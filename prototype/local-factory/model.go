package main

import "time"

type issue struct {
	Repository string   `json:"repository"`
	Number     int      `json:"number"`
	Title      string   `json:"title"`
	Body       string   `json:"body"`
	URL        string   `json:"url"`
	Labels     []string `json:"labels,omitempty"`
}

type event struct {
	At      time.Time `json:"at"`
	Message string    `json:"message"`
}

type work struct {
	ID          string    `json:"id"`
	Issue       issue     `json:"issue"`
	State       string    `json:"state"`
	Attempt     int       `json:"attempt"`
	ActiveRole  string    `json:"active_role,omitempty"`
	Branch      string    `json:"branch,omitempty"`
	Workspace   string    `json:"workspace,omitempty"`
	HeadSHA     string    `json:"head_sha,omitempty"`
	VerifiedSHA string    `json:"verified_sha,omitempty"`
	VerifyRuns  int       `json:"verify_runs,omitempty"`
	PRURL       string    `json:"pr_url,omitempty"`
	Failure     string    `json:"failure,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	CompletedAt time.Time `json:"completed_at,omitempty"`
	Events      []event   `json:"events"`
}

const (
	stateQueued  = "queued"
	stateRunning = "running"
	stateBlocked = "blocked"
	stateReady   = "ready"
	stateFailed  = "failed"
)
