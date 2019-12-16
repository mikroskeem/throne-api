package main

import (
	"context"
	"database/sql"
	"io/ioutil"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"time"

	"github.com/BurntSushi/toml"
	_ "github.com/go-sql-driver/mysql"
	"github.com/gorilla/mux"
	"go.uber.org/zap"
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

	endpoints := Endpoints{db: db}

	// Set up HTTP server
	router := mux.NewRouter()
	router.HandleFunc("/api/v1/votes", endpoints.HandleVoters)
	router.HandleFunc("/api/v1/staff", endpoints.HandleStaff)
	router.HandleFunc("/api/v1/player/{player}", endpoints.HandlePlayer)

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
}
