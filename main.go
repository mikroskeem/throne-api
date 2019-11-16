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
	config throneAPIConfig
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
			playerGroups := map[string]string{}

			// Query player primary groups
			rows1, err := db.QueryContext(ctx,
				fmt.Sprintf("select username, primary_group from %s.%splayers;",
					config.Database.LuckPermsDatabaseName,
					config.Database.LuckPermsTablePrefix))
			if err != nil {
				resultCh <- err
				return
			}
			defer rows1.Close()

			var username string
			var primaryGroup string
			for rows1.Next() {
				if err := rows1.Scan(&username, &primaryGroup); err != nil {
					zap.L().Warn("failed to scan row", zap.Error(err))
					continue
				}

				// Filter out only relevant groups
				for _, relevant := range config.Database.StaffGroupNames {
					if relevant == primaryGroup {
						playerGroups[username] = primaryGroup
						break
					}
				}
			}

			// Query group title and color
			seenGroupNames := map[string]bool{}
			groupNamesQuery := strings.Builder{}
			var queryPart string
			for _, groupName := range playerGroups {
				if _, ok := seenGroupNames[groupName]; !ok {
					seenGroupNames[groupName] = true
					fmt.Fprintf(&groupNamesQuery, "name = '%s' or ", groupName)
				}
			}

			rows2, err := db.QueryContext(ctx,
				fmt.Sprintf("select name, permission from %s.%sgroup_permissions where (%s) and "+
					"(permission like 'prefix.%%' or permission like 'weight.%%');",
					config.Database.LuckPermsDatabaseName,
					config.Database.LuckPermsTablePrefix,
					groupNamesQuery.String()[:groupNamesQuery.Len()-4]))
			if err != nil {
				resultCh <- err
				return
			}
			defer rows2.Close()

			var groupName string
			var permissionNode string
			for rows2.Next() {
				if err := rows2.Scan(&groupName, &permissionNode); err != nil {
					zap.L().Warn("failed to scan row", zap.Error(err))
					continue
				}

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
		writeResponse(w, http.StatusNotImplemented, "not done yet")
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
