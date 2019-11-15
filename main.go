package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/BurntSushi/toml"
	_ "github.com/go-sql-driver/mysql"
	"github.com/gorilla/mux"
	"go.uber.org/zap"
)

type ErrorResponse struct {
	Message string `json:"error"`
}

func main() {
	defer zap.L().Sync()
	zap.L().Info("hello world")

	databaseURL := "" // TODO: read from the configuration
	db, err := sql.Open("mysql", databaseURL)
	if err != nil {
		zap.L().Panic("failed to open database connection", zap.Error(err))
	}

	// Set up HTTP server
	router := mux.NewRouter()
	router.HandleFunc("/api/v1/votes", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ErrorResponse{"not done yet"})
	})

	router.HandleFunc("/api/v1/staff", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ErrorResponse{"not done yet"})
	})

	router.HandleFunc("/api/v1/staff", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ErrorResponse{"not done yet"})
	})

	srv := &http.Server{
		Handler:      router,
		WriteTimeout: 15 * time.Second,
		ReadTimeout:  15 * time.Second,
	}

	// TODO: continue
	var _ = toml.Decode
	var _ = db
	var _ = srv
}
