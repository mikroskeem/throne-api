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

type VoterInfo struct {
	Username string `json:"voter_name"`
	Votes    int    `json:"votes"`
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

	var config throneAPIConfig
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
		// 3 seconds to query the voters and process the data. Should be fine?
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		resultCh := make(chan interface{}, 1)

		go func() {
			rows, err := db.QueryContext(ctx,
				// Pls no bully but prepared statements are not needed here - not handling user input, technically
				fmt.Sprintf("select voter_name, votes from %s.%s order by votes desc;",
					config.Database.ConfettiDatabaseName,
					config.Database.ConfettiVotesTableName))
			if err != nil {
				resultCh <- err
				return
			}
			defer rows.Close()

			voters := []VoterInfo{}
			for rows.Next() {
				voter := VoterInfo{}
				if err := rows.Scan(&(voter.Username), &(voter.Votes)); err != nil {
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
