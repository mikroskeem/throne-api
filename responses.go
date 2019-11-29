package main

const (
	errorStatus = "error"
	okStatus    = "ok"
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
