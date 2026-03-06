package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

// StateProvider provides runtime state snapshots to the server.
type StateProvider interface {
	GetStateSnapshot() StateSnapshot
	GetIssueDetail(identifier string) (*IssueDetail, error)
	RequestRefresh()
}

// StateSnapshot represents the system state for the JSON API.
type StateSnapshot struct {
	GeneratedAt string           `json:"generated_at"`
	Counts      SnapshotCounts   `json:"counts"`
	Running     []RunningRow     `json:"running"`
	Retrying    []RetryRow       `json:"retrying"`
	CodexTotals CodexTotalsJSON  `json:"codex_totals"`
	RateLimits  interface{}      `json:"rate_limits"`
}

type SnapshotCounts struct {
	Running  int `json:"running"`
	Retrying int `json:"retrying"`
}

type RunningRow struct {
	IssueID         string        `json:"issue_id"`
	IssueIdentifier string        `json:"issue_identifier"`
	State           string        `json:"state"`
	SessionID       string        `json:"session_id"`
	TurnCount       int           `json:"turn_count"`
	LastEvent       string        `json:"last_event"`
	LastMessage     string        `json:"last_message"`
	StartedAt       string        `json:"started_at"`
	LastEventAt     *string       `json:"last_event_at"`
	Tokens          TokensJSON    `json:"tokens"`
}

type RetryRow struct {
	IssueID         string `json:"issue_id"`
	IssueIdentifier string `json:"issue_identifier"`
	Attempt         int    `json:"attempt"`
	DueAt           string `json:"due_at"`
	Error           string `json:"error"`
}

type CodexTotalsJSON struct {
	InputTokens    int64   `json:"input_tokens"`
	OutputTokens   int64   `json:"output_tokens"`
	TotalTokens    int64   `json:"total_tokens"`
	SecondsRunning float64 `json:"seconds_running"`
}

type TokensJSON struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	TotalTokens  int64 `json:"total_tokens"`
}

// IssueDetail holds issue-specific runtime/debug details.
type IssueDetail struct {
	IssueIdentifier string      `json:"issue_identifier"`
	IssueID         string      `json:"issue_id"`
	Status          string      `json:"status"`
	Workspace       *WsInfo     `json:"workspace"`
	Attempts        *AttemptInfo `json:"attempts"`
	Running         *RunningRow `json:"running"`
	Retry           *RetryRow   `json:"retry"`
	LastError       *string     `json:"last_error"`
}

type WsInfo struct {
	Path string `json:"path"`
}

type AttemptInfo struct {
	RestartCount       int `json:"restart_count"`
	CurrentRetryAttempt int `json:"current_retry_attempt"`
}

// Server implements the optional HTTP server extension (Section 13.7).
type Server struct {
	provider StateProvider
	port     int
	listener net.Listener
	mux      *http.ServeMux
}

// New creates a new Server.
func New(provider StateProvider, port int) *Server {
	s := &Server{
		provider: provider,
		port:     port,
		mux:      http.NewServeMux(),
	}
	s.registerRoutes()
	return s
}

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("/", s.handleDashboard)
	s.mux.HandleFunc("/api/v1/state", s.handleState)
	s.mux.HandleFunc("/api/v1/refresh", s.handleRefresh)
	s.mux.HandleFunc("/api/v1/", s.handleIssueDetail)
}

// Start starts the HTTP server and returns the listener address.
func (s *Server) Start() (string, error) {
	addr := fmt.Sprintf("127.0.0.1:%d", s.port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", fmt.Errorf("listen %s: %w", addr, err)
	}
	s.listener = ln
	actualAddr := ln.Addr().String()
	slog.Info("HTTP server started", "addr", actualAddr)

	go func() {
		srv := &http.Server{Handler: s.mux}
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Error("HTTP server error", "error", err)
		}
	}()

	return actualAddr, nil
}

// Addr returns the listener address, or empty if not started.
func (s *Server) Addr() string {
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	snapshot := s.provider.GetStateSnapshot()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, dashboardHTML(snapshot))
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only GET is allowed")
		return
	}
	snapshot := s.provider.GetStateSnapshot()
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only POST is allowed")
		return
	}

	s.provider.RequestRefresh()

	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"queued":       true,
		"coalesced":    false,
		"requested_at": time.Now().UTC().Format(time.RFC3339),
		"operations":   []string{"poll", "reconcile"},
	})
}

func (s *Server) handleIssueDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only GET is allowed")
		return
	}

	// Extract identifier from path /api/v1/<identifier>
	identifier := strings.TrimPrefix(r.URL.Path, "/api/v1/")
	if identifier == "" || identifier == "state" || identifier == "refresh" {
		writeJSONError(w, http.StatusNotFound, "not_found", "Unknown endpoint")
		return
	}

	detail, err := s.provider.GetIssueDetail(identifier)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "issue_not_found", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, detail)
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}

func dashboardHTML(snap StateSnapshot) string {
	var running string
	for _, r := range snap.Running {
		lastEvent := ""
		if r.LastEventAt != nil {
			lastEvent = *r.LastEventAt
		}
		running += fmt.Sprintf(`<tr>
			<td>%s</td><td>%s</td><td>%s</td><td>%s</td>
			<td>%d</td><td>%s</td><td>%s</td>
			<td>%d/%d/%d</td>
		</tr>`,
			r.IssueIdentifier, r.State, r.SessionID, r.StartedAt,
			r.TurnCount, r.LastEvent, lastEvent,
			r.Tokens.InputTokens, r.Tokens.OutputTokens, r.Tokens.TotalTokens,
		)
	}

	var retrying string
	for _, r := range snap.Retrying {
		retrying += fmt.Sprintf(`<tr>
			<td>%s</td><td>%d</td><td>%s</td><td>%s</td>
		</tr>`, r.IssueIdentifier, r.Attempt, r.DueAt, r.Error)
	}

	return fmt.Sprintf(`<!DOCTYPE html>
<html><head>
<meta charset="utf-8">
<title>Symphony Dashboard</title>
<meta http-equiv="refresh" content="5">
<style>
body { font-family: system-ui, sans-serif; margin: 2rem; background: #1a1a2e; color: #eee; }
h1 { color: #e94560; }
h2 { color: #0f3460; background: #16213e; padding: 0.5rem 1rem; border-radius: 4px; color: #e94560; }
table { border-collapse: collapse; width: 100%%; margin-bottom: 2rem; }
th, td { border: 1px solid #333; padding: 0.5rem; text-align: left; }
th { background: #16213e; }
tr:nth-child(even) { background: #1a1a3e; }
.stats { display: flex; gap: 2rem; margin-bottom: 2rem; }
.stat { background: #16213e; padding: 1rem 2rem; border-radius: 8px; }
.stat-value { font-size: 2rem; font-weight: bold; color: #e94560; }
.stat-label { color: #888; }
</style>
</head><body>
<h1>Symphony Dashboard</h1>
<p>Generated: %s</p>

<div class="stats">
<div class="stat"><div class="stat-value">%d</div><div class="stat-label">Running</div></div>
<div class="stat"><div class="stat-value">%d</div><div class="stat-label">Retrying</div></div>
<div class="stat"><div class="stat-value">%d</div><div class="stat-label">Total Tokens</div></div>
<div class="stat"><div class="stat-value">%.1f</div><div class="stat-label">Runtime (s)</div></div>
</div>

<h2>Running Sessions</h2>
<table>
<tr><th>Issue</th><th>State</th><th>Session</th><th>Started</th><th>Turns</th><th>Last Event</th><th>Last Event At</th><th>Tokens (in/out/total)</th></tr>
%s
</table>

<h2>Retry Queue</h2>
<table>
<tr><th>Issue</th><th>Attempt</th><th>Due At</th><th>Error</th></tr>
%s
</table>

<h2>Totals</h2>
<table>
<tr><th>Input Tokens</th><th>Output Tokens</th><th>Total Tokens</th><th>Runtime (seconds)</th></tr>
<tr><td>%d</td><td>%d</td><td>%d</td><td>%.1f</td></tr>
</table>

</body></html>`,
		snap.GeneratedAt,
		snap.Counts.Running, snap.Counts.Retrying,
		snap.CodexTotals.TotalTokens, snap.CodexTotals.SecondsRunning,
		running, retrying,
		snap.CodexTotals.InputTokens, snap.CodexTotals.OutputTokens,
		snap.CodexTotals.TotalTokens, snap.CodexTotals.SecondsRunning,
	)
}
