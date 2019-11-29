package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"go.uber.org/zap"
)

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

type Endpoints struct {
	db *sql.DB
}

func (e *Endpoints) HandleVoters(w http.ResponseWriter, r *http.Request) {
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
		rows, err := e.db.QueryContext(ctx,
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
}

func (e *Endpoints) HandleStaff(w http.ResponseWriter, r *http.Request) {
}

func (e *Endpoints) HandlePlayer(w http.ResponseWriter, r *http.Request) {
}
