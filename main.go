package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	_ "github.com/go-sql-driver/mysql"
	"github.com/gorilla/mux"
	"go.uber.org/zap"
)

const (
	errorStatus = "error"
	okStatus    = "ok"
)

var (
	config           throneAPIConfig
	checkedRankNames = make(map[string]bool)
	chatColorRegexp  = regexp.MustCompile("(?i)[&§][0-9A-FK-OR]")
	chatColorsToHex  = map[string]string{
		"0": "#000000",
		"1": "#0000AA",
		"2": "#00AA00",
		"3": "#00AAAA",
		"4": "#AA0000",
		"5": "#AA00AA",
		"6": "#FFAA00",
		"7": "#AAAAAA",
		"8": "#555555",
		"9": "#5555FF",
		"a": "#55FF55",
		"b": "#55FFFF",
		"c": "#FF5555",
		"d": "#FF55FF",
		"e": "#FFFF55",
		"f": "#FFFFFF",
	}
)

type VoterInfo struct {
	Username  string `json:"voter_name"`
	Votes     int    `json:"votes"`
	Timestamp uint64 `json:"last_vote_timestamp"`
}

type StaffInfo struct {
	Groups map[string]GroupInfo `json:"groups"`
}

type GroupInfo struct {
	Title   string   `json:"title"`
	Color   string   `json:"color"`
	Weight  int      `json:"weight"`
	Members []string `json:"members"`
}

type StatusResponse struct {
	Status string      `json:"status"`
	Data   interface{} `json:"data"`
}

func writeResponse(w http.ResponseWriter, status int, body interface{}) {
	var stringStatus string
	if status == http.StatusOK {
		stringStatus = okStatus
	} else {
		stringStatus = errorStatus
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", config.RestAPI.CORSOrigins)
	w.Header().Set("Access-Control-Allow-Methods", "GET")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(StatusResponse{stringStatus, body})
}

func main() {
	var err error
	if logger, err := zap.NewProduction(); err == nil {
		zap.ReplaceGlobals(logger)
	} else {
		panic(err)
	}
	defer zap.L().Sync()
	zap.L().Info("hello world")

	// Load configuration
	var rawConfig []byte
	if rawConfig, err = ioutil.ReadFile("./config.toml"); err != nil {
		zap.L().Panic("failed to read configuration", zap.Error(err))
	}

	if err = toml.Unmarshal(rawConfig, &config); err != nil {
		zap.L().Panic("failed to parse configuration", zap.Error(err))
	}

	// Put together rank names map for easier checking
	for _, rankName := range config.Database.StaffGroupNames {
		checkedRankNames[rankName] = true
	}

	// Connect to the database
	var db *sql.DB
	if db, err = sql.Open("mysql", config.Database.DatabaseURL); err != nil {
		zap.L().Panic("failed to open database connection", zap.Error(err))
	}
	db.SetMaxOpenConns(32)
	db.SetMaxIdleConns(64)
	db.SetConnMaxLifetime(5 * time.Minute)
	defer db.Close()

	// Test databse connection
	if err := db.Ping(); err != nil {
		zap.L().Panic("failed to test database connection", zap.Error(err))
	} else {
		zap.L().Info("database connection works")
	}

	// Set up HTTP server
	router := mux.NewRouter()
	router.HandleFunc("/api/v1/votes", func(w http.ResponseWriter, r *http.Request) {
		votersLimit := -1
		if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
			if num, err := strconv.Atoi(limitStr); err == nil && num > 0 {
				votersLimit = num
			} else {
				writeResponse(w, http.StatusBadRequest, fmt.Sprintf("invalid limit: %s", limitStr))
				return
			}
		}

		// 3 seconds to query the voters and process the data. Should be fine?
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		resultCh := make(chan interface{}, 1)

		go func() {
			var limitStr string
			if votersLimit != -1 {
				limitStr = fmt.Sprintf("limit %d", votersLimit)
			} else {
				limitStr = ""
			}
			rows, err := db.QueryContext(ctx,
				// Pls no bully but prepared statements are not needed here - not handling user input, technically
				fmt.Sprintf("select voter_name, votes, last_vote_timestamp from %s.%s order by votes desc %s;",
					config.Database.ConfettiDatabaseName,
					config.Database.ConfettiVotesTableName,
					limitStr))
			if err != nil {
				resultCh <- err
				return
			}
			defer rows.Close()

			voters := []VoterInfo{}
			for rows.Next() {
				voter := VoterInfo{}
				if err := rows.Scan(&(voter.Username), &(voter.Votes), &(voter.Timestamp)); err != nil {
					zap.L().Warn("failed to scan row", zap.Error(err))
					continue
				}
				voters = append(voters, voter)
			}

			resultCh <- voters
		}()

		select {
		case result := <-resultCh:
			if err, ok := result.(error); ok {
				zap.L().Error("failed to fetch votes", zap.Error(err))
				writeResponse(w, http.StatusInternalServerError, "database access error")
			} else {
				writeResponse(w, http.StatusOK, result)
			}
		case <-ctx.Done():
			zap.L().Error("timed out while getting or processing database entries")
			writeResponse(w, http.StatusInternalServerError, "timed out")
		}
	})

	router.HandleFunc("/api/v1/staff", func(w http.ResponseWriter, r *http.Request) {
		// 5 seconds to query the groups and players, and finally process the data. Should be enough
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resultCh := make(chan interface{}, 1)

		go func() {
			collectedRanks := map[string]*GroupInfo{}
			primaryGroupsScanned := make(chan map[string]*GroupInfo, 1)
			userPermissionsScanned := make(chan map[string]*GroupInfo, 1)

			// Collect groups and their members from players table
			go func() {
				rows1, err := db.QueryContext(ctx,
					// TODO: let database do the work and filter out unwanted groups
					fmt.Sprintf("select username, primary_group from %s.%splayers;",
						config.Database.LuckPermsDatabaseName,
						config.Database.LuckPermsTablePrefix))
				if err != nil {
					resultCh <- err
					return
				}
				defer rows1.Close()

				collected := map[string]*GroupInfo{}

				var username string
				var primaryGroup string
				for rows1.Next() {
					if err := rows1.Scan(&username, &primaryGroup); err != nil {
						zap.L().Warn("failed to scan row", zap.Error(err))
						continue
					}

					// Filter players out only from relevant groups
					if _, ok := checkedRankNames[primaryGroup]; !ok {
						continue
					}

					if _, ok := collected[primaryGroup]; !ok {
						collected[primaryGroup] = &GroupInfo{}
					}

					collected[primaryGroup].Members = append(collected[primaryGroup].Members, username)
				}

				primaryGroupsScanned <- collected
			}()

			// Collect groups from user permissions
			go func() {
				rows2, err := db.QueryContext(ctx,
					// TODO: let database do the work and filter out unwanted groups
					fmt.Sprintf("select permission, (select %[1]s.%[2]splayers.username from %[1]s.%[2]splayers where "+
						"%[1]s.%[2]splayers.uuid = %[1]s.%[2]suser_permissions.uuid) as name from "+
						"%[1]s.%[2]suser_permissions where permission like 'group.%%';",
						config.Database.LuckPermsDatabaseName,
						config.Database.LuckPermsTablePrefix))
				if err != nil {
					resultCh <- err
					return
				}
				defer rows2.Close()

				collected := map[string]*GroupInfo{}

				var permissionNode string
				var username string
				for rows2.Next() {
					if err := rows2.Scan(&permissionNode, &username); err != nil {
						zap.L().Warn("failed to scan row", zap.Error(err))
						continue
					}

					split := strings.Split(permissionNode, ".")
					if len(split) != 2 {
						zap.L().Warn("unable to parse group permission node", zap.String("node", permissionNode))
						continue
					}
					rankName := split[1]

					// Filter players out only from relevant groups
					if _, ok := checkedRankNames[rankName]; !ok {
						continue
					}

					if _, ok := collected[rankName]; !ok {
						collected[rankName] = &GroupInfo{}
					}

					collected[rankName].Members = append(collected[rankName].Members, username)
				}

				userPermissionsScanned <- collected
			}()

			// Wait for primary groups scan
			if s := <-primaryGroupsScanned; s != nil {
				for k, v := range s {
					collectedRanks[k] = v
				}
			}

			// Wait for user permissions scan
			if s := <-userPermissionsScanned; s != nil {
				for k, v := range s {
					if rank, ok := collectedRanks[k]; ok {
						for _, m := range v.Members {
							// TODO: deduplicate
							rank.Members = append(rank.Members, m)
						}
					} else {
						collectedRanks[k] = v
					}
				}
			}

			// Query group title and color
			var groupNamesQuery strings.Builder
			if len(collectedRanks) > 0 {
				for rankName := range collectedRanks {
					fmt.Fprintf(&groupNamesQuery, "name = '%s' or ", rankName)
				}
			} else {
				// Write atleast one valid SQL value to avoid syntax error + ' or ' to make slicing work fine
				groupNamesQuery.WriteString("1 or ")
			}

			rows3, err := db.QueryContext(ctx,
				fmt.Sprintf(
					"select name, permission from %s.%sgroup_permissions where (%s) and "+
						"(permission like 'prefix.%%' or permission like 'weight.%%');",
					config.Database.LuckPermsDatabaseName,
					config.Database.LuckPermsTablePrefix,
					groupNamesQuery.String()[:groupNamesQuery.Len()-4]))
			if err != nil {
				resultCh <- err
				return
			}
			defer rows3.Close()

			var groupName string
			var permissionNode string
			for rows3.Next() {
				if err := rows3.Scan(&groupName, &permissionNode); err != nil {
					zap.L().Warn("failed to scan row", zap.Error(err))
					continue
				}

				split := strings.Split(permissionNode, ".")

				switch split[0] {
				case "weight":
					if num, err := strconv.Atoi(split[1]); err == nil {
						if rank, ok := collectedRanks[groupName]; ok {
							rank.Weight = num
						} else {
							zap.L().Error("got weight for unknown group", zap.String("node", permissionNode), zap.String("groupName", groupName))
						}

					}
				case "prefix":
					var minecraftPrefix string
					switch len(split) {
					case 2:
						minecraftPrefix = split[1]
					case 3:
						minecraftPrefix = split[2]
					default:
						zap.L().Warn("could not get rank prefix", zap.String("rankName", groupName))
						minecraftPrefix = ""
					}

					if rank, ok := collectedRanks[groupName]; ok {
						// Get rank color by getting last color code
						// Not perfect but most likely works
						colorMatches := chatColorRegexp.FindAllString(minecraftPrefix, -1)
						if len(colorMatches) > 0 {
							foundColor := strings.ToLower(colorMatches[len(colorMatches)-1][1:])
							if hexColor, ok := chatColorsToHex[foundColor]; ok {
								rank.Color = hexColor
							}
						}

						// Get rank title by stripping minecraft color codes
						rank.Title = chatColorRegexp.ReplaceAllString(minecraftPrefix, "")
					} else {
						zap.L().Error("got prefix for unknown group", zap.String("node", permissionNode), zap.String("groupName", groupName))
					}

				}
			}

			resultCh <- collectedRanks
		}()

		select {
		case result := <-resultCh:
			if err, ok := result.(error); ok {
				zap.L().Error("failed to fetch votes", zap.Error(err))
				writeResponse(w, http.StatusInternalServerError, "database access error")
			} else {
				writeResponse(w, http.StatusOK, result)
			}
		case <-ctx.Done():
			zap.L().Error("timed out while getting or processing database entries")
			writeResponse(w, http.StatusInternalServerError, "timed out")
		}
	})

	router.HandleFunc("/api/v1/player/{player}", func(w http.ResponseWriter, r *http.Request) {
		writeResponse(w, http.StatusNotImplemented, "not done yet")
	})

	srv := &http.Server{
		Addr:         config.RestAPI.ListenAddress,
		Handler:      router,
		WriteTimeout: 15 * time.Second,
		ReadTimeout:  15 * time.Second,
	}

	// Set up signal handler
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)

	exitCh := make(chan bool, 1)
	go func() {
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			zap.L().Error("failed to serve http", zap.Error(err))
		}
		exitCh <- true
	}()

	select {
	case <-sig:
		zap.L().Info("signal caught, exiting")
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*2)
		defer cancel()

		shutdownCh := make(chan bool, 1)
		go func() {
			srv.Shutdown(ctx)
			shutdownCh <- true
		}()

		select {
		case <-shutdownCh:
			// yay
		case <-ctx.Done():
			zap.L().Info("timed out while waiting server to close, killing it forcefully")
			srv.Close()
		}
	case <-exitCh:
		zap.L().Info("exiting")
	}

	os.Exit(0)
}
