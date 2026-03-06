package domain

import (
	"sync"
	"time"
)

// Issue - normalized issue record (Section 4.1.1)
type Issue struct {
	ID          string       `json:"id"`
	Identifier  string       `json:"identifier"`
	Title       string       `json:"title"`
	Description *string      `json:"description"`
	Priority    *int         `json:"priority"`
	State       string       `json:"state"`
	BranchName  *string      `json:"branch_name"`
	URL         *string      `json:"url"`
	Labels      []string     `json:"labels"`
	BlockedBy   []BlockerRef `json:"blocked_by"`
	CreatedAt   *time.Time   `json:"created_at"`
	UpdatedAt   *time.Time   `json:"updated_at"`
}

type BlockerRef struct {
	ID         *string `json:"id"`
	Identifier *string `json:"identifier"`
	State      *string `json:"state"`
}

// WorkflowDefinition - parsed WORKFLOW.md (Section 4.1.2)
type WorkflowDefinition struct {
	Config         map[string]interface{} `json:"config"`
	PromptTemplate string                 `json:"prompt_template"`
}

// Workspace (Section 4.1.4)
type Workspace struct {
	Path         string `json:"path"`
	WorkspaceKey string `json:"workspace_key"`
	CreatedNow   bool   `json:"created_now"`
}

// RunAttempt (Section 4.1.5)
type RunAttempt struct {
	IssueID         string    `json:"issue_id"`
	IssueIdentifier string    `json:"issue_identifier"`
	Attempt         *int      `json:"attempt"`
	WorkspacePath   string    `json:"workspace_path"`
	StartedAt       time.Time `json:"started_at"`
	Status          string    `json:"status"`
	Error           string    `json:"error,omitempty"`
}

// LiveSession (Section 4.1.6) - tracks coding agent subprocess state
type LiveSession struct {
	SessionID                string     `json:"session_id"`
	ThreadID                 string     `json:"thread_id"`
	TurnID                   string     `json:"turn_id"`
	CodexAppServerPID        string     `json:"codex_app_server_pid,omitempty"`
	LastCodexEvent           string     `json:"last_codex_event,omitempty"`
	LastCodexTimestamp        *time.Time `json:"last_codex_timestamp,omitempty"`
	LastCodexMessage         string     `json:"last_codex_message,omitempty"`
	CodexInputTokens         int64      `json:"codex_input_tokens"`
	CodexOutputTokens        int64      `json:"codex_output_tokens"`
	CodexTotalTokens         int64      `json:"codex_total_tokens"`
	LastReportedInputTokens  int64      `json:"last_reported_input_tokens"`
	LastReportedOutputTokens int64      `json:"last_reported_output_tokens"`
	LastReportedTotalTokens  int64      `json:"last_reported_total_tokens"`
	TurnCount                int        `json:"turn_count"`
}

// RetryEntry (Section 4.1.7)
type RetryEntry struct {
	IssueID    string      `json:"issue_id"`
	Identifier string      `json:"identifier"`
	Attempt    int         `json:"attempt"`
	DueAtMs    int64       `json:"due_at_ms"`
	Timer      *time.Timer `json:"-"`
	Error      string      `json:"error,omitempty"`
}

// RunningEntry - entry in orchestrator running map
type RunningEntry struct {
	Issue        *Issue      `json:"issue"`
	Identifier   string      `json:"identifier"`
	Session      LiveSession `json:"session"`
	RetryAttempt *int        `json:"retry_attempt"`
	StartedAt    time.Time   `json:"started_at"`
	Cancel       func()      `json:"-"` // cancel function for the worker goroutine context
}

// CodexTotals - aggregate token and runtime totals
type CodexTotals struct {
	InputTokens    int64   `json:"input_tokens"`
	OutputTokens   int64   `json:"output_tokens"`
	TotalTokens    int64   `json:"total_tokens"`
	SecondsRunning float64 `json:"seconds_running"`
}

// OrchestratorState (Section 4.1.8)
type OrchestratorState struct {
	mu                  sync.Mutex
	PollIntervalMs      int                      `json:"poll_interval_ms"`
	MaxConcurrentAgents int                      `json:"max_concurrent_agents"`
	Running             map[string]*RunningEntry `json:"running"`
	Claimed             map[string]bool          `json:"claimed"`
	RetryAttempts       map[string]*RetryEntry   `json:"retry_attempts"`
	Completed           map[string]bool          `json:"completed"`
	CodexTotals         CodexTotals              `json:"codex_totals"`
	CodexRateLimits     interface{}              `json:"codex_rate_limits"`
}

func NewOrchestratorState(pollIntervalMs, maxConcurrent int) *OrchestratorState {
	return &OrchestratorState{
		PollIntervalMs:      pollIntervalMs,
		MaxConcurrentAgents: maxConcurrent,
		Running:             make(map[string]*RunningEntry),
		Claimed:             make(map[string]bool),
		RetryAttempts:       make(map[string]*RetryEntry),
		Completed:           make(map[string]bool),
	}
}

func (s *OrchestratorState) Lock()   { s.mu.Lock() }
func (s *OrchestratorState) Unlock() { s.mu.Unlock() }

// CodexUpdate - event from coding agent to orchestrator
type CodexUpdate struct {
	IssueID    string
	Event      string
	Timestamp  time.Time
	PID        string
	Message    string
	Usage      *TokenUsage
	RateLimits interface{}
}

type TokenUsage struct {
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
}

// Run attempt statuses (Section 7.2)
const (
	StatusPreparingWorkspace    = "preparing_workspace"
	StatusBuildingPrompt        = "building_prompt"
	StatusLaunchingAgentProcess = "launching_agent_process"
	StatusInitializingSession   = "initializing_session"
	StatusStreamingTurn         = "streaming_turn"
	StatusFinishing             = "finishing"
	StatusSucceeded             = "succeeded"
	StatusFailed                = "failed"
	StatusTimedOut              = "timed_out"
	StatusStalled               = "stalled"
	StatusCanceledByRecon       = "canceled_by_reconciliation"
)

// Agent events (Section 10.4)
const (
	EventSessionStarted       = "session_started"
	EventStartupFailed        = "startup_failed"
	EventTurnCompleted        = "turn_completed"
	EventTurnFailed           = "turn_failed"
	EventTurnCancelled        = "turn_cancelled"
	EventTurnEndedWithError   = "turn_ended_with_error"
	EventTurnInputRequired    = "turn_input_required"
	EventApprovalAutoApproved = "approval_auto_approved"
	EventUnsupportedToolCall  = "unsupported_tool_call"
	EventNotification         = "notification"
	EventOtherMessage         = "other_message"
	EventMalformed            = "malformed"
)
