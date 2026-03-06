// Package orchestrator implements the Symphony poll-dispatch-reconcile loop.
// It is the single authority for state mutations: no duplicate dispatches,
// mutex-protected state, context-driven cancellation throughout.
package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/monlabs/symphony/internal/agent"
	"github.com/monlabs/symphony/internal/config"
	"github.com/monlabs/symphony/internal/domain"
	"github.com/monlabs/symphony/internal/server"
	"github.com/monlabs/symphony/internal/tracker"
	"github.com/monlabs/symphony/internal/workspace"
)

// ---------------------------------------------------------------------------
// Snapshot types for the HTTP API
// ---------------------------------------------------------------------------

// StateSnapshot is the read-only view returned by GetState for the HTTP API.
type StateSnapshot struct {
	GeneratedAt time.Time          `json:"generated_at"`
	Running     []RunningSnapshot  `json:"running"`
	Retrying    []RetrySnapshot    `json:"retrying"`
	CodexTotals domain.CodexTotals `json:"codex_totals"`
	RateLimits  interface{}        `json:"rate_limits"`
}

// RunningSnapshot describes a single running worker visible to the API.
type RunningSnapshot struct {
	IssueID         string           `json:"issue_id"`
	IssueIdentifier string           `json:"issue_identifier"`
	State           string           `json:"state"`
	SessionID       string           `json:"session_id"`
	TurnCount       int              `json:"turn_count"`
	LastEvent       string           `json:"last_event"`
	LastMessage     string           `json:"last_message"`
	StartedAt       time.Time        `json:"started_at"`
	LastEventAt     *time.Time       `json:"last_event_at"`
	Tokens          domain.TokenUsage `json:"tokens"`
}

// RetrySnapshot describes a single entry in the retry queue visible to the API.
type RetrySnapshot struct {
	IssueID         string    `json:"issue_id"`
	IssueIdentifier string    `json:"issue_identifier"`
	Attempt         int       `json:"attempt"`
	DueAt           time.Time `json:"due_at"`
	Error           string    `json:"error"`
}

// ---------------------------------------------------------------------------
// workerResult is sent from a worker goroutine back to the orchestrator.
// ---------------------------------------------------------------------------
type workerResult struct {
	issueID    string
	identifier string
	attempt    int
	err        error
}

// ---------------------------------------------------------------------------
// Orchestrator
// ---------------------------------------------------------------------------

// Orchestrator implements the poll-dispatch-reconcile loop described in the spec.
type Orchestrator struct {
	state         *domain.OrchestratorState
	config        *config.ServiceConfig
	workflow      *config.WorkflowDefinition
	workflowPath  string
	tracker       tracker.Client
	workspaceMgr  *workspace.Manager
	promptBuilder *agent.DefaultPromptBuilder

	refreshCh chan struct{}
	stopCh    chan struct{}

	// workerDoneCh receives results from exiting workers.
	workerDoneCh chan workerResult

	// workflowMtime tracks WORKFLOW.md modification time for dynamic reload.
	workflowMtime time.Time

	// logger for structured output.
	logger *slog.Logger
}

// New creates a new Orchestrator by loading the workflow file, parsing config,
// validating dispatch prerequisites, and constructing all dependencies.
func New(workflowPath string, portOverride *int) (*Orchestrator, error) {
	logger := slog.Default()

	// Load and parse WORKFLOW.md.
	wf, err := config.LoadWorkflow(workflowPath)
	if err != nil {
		return nil, fmt.Errorf("load workflow: %w", err)
	}

	cfg := config.ParseServiceConfig(wf.Config)

	// Apply port override if provided.
	if portOverride != nil {
		cfg.Server.Port = portOverride
	}

	// Validate dispatch prerequisites.
	if err := config.ValidateDispatchConfig(cfg); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	// Create tracker client.
	tc, err := tracker.NewClient(
		cfg.Tracker.Kind,
		cfg.Tracker.Endpoint,
		cfg.Tracker.APIKey,
		cfg.Tracker.ProjectSlug,
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)
	if err != nil {
		return nil, fmt.Errorf("create tracker client: %w", err)
	}

	// Create workspace manager.
	wsMgr := workspace.NewManager(cfg.Workspace.Root, workspace.HooksConfig{
		AfterCreate:  cfg.Hooks.AfterCreate,
		BeforeRun:    cfg.Hooks.BeforeRun,
		AfterRun:     cfg.Hooks.AfterRun,
		BeforeRemove: cfg.Hooks.BeforeRemove,
		TimeoutMs:    cfg.Hooks.TimeoutMs,
	})

	// Create prompt builder.
	pb := agent.NewDefaultPromptBuilder(wf.PromptTemplate)

	// Record initial workflow mtime.
	var mtime time.Time
	if fi, err := os.Stat(workflowPath); err == nil {
		mtime = fi.ModTime()
	}

	state := domain.NewOrchestratorState(cfg.Polling.IntervalMs, cfg.Agent.MaxConcurrentAgents)

	return &Orchestrator{
		state:         state,
		config:        cfg,
		workflow:      wf,
		workflowPath:  workflowPath,
		tracker:       tc,
		workspaceMgr:  wsMgr,
		promptBuilder: pb,
		refreshCh:     make(chan struct{}, 1),
		stopCh:        make(chan struct{}),
		workerDoneCh:  make(chan workerResult, 64),
		workflowMtime: mtime,
		logger:        logger,
	}, nil
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

// Run executes the main orchestrator loop. It blocks until ctx is cancelled
// or an unrecoverable error occurs.
func (o *Orchestrator) Run(ctx context.Context) error {
	o.logger.Info("orchestrator starting",
		"workflow", o.workflowPath,
		"poll_interval_ms", o.config.Polling.IntervalMs,
		"max_concurrent", o.config.Agent.MaxConcurrentAgents,
	)

	// Start HTTP server if configured.
	o.startHTTPServer()

	// Startup: terminal cleanup.
	o.startupTerminalCleanup(ctx)

	// Immediate first tick.
	o.tick(ctx)

	pollInterval := time.Duration(o.config.Polling.IntervalMs) * time.Millisecond
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	// File-watch ticker for WORKFLOW.md dynamic reload (every 5 seconds).
	reloadTicker := time.NewTicker(5 * time.Second)
	defer reloadTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			o.logger.Info("orchestrator shutting down", "reason", ctx.Err())
			o.shutdownWorkers()
			return ctx.Err()

		case <-o.stopCh:
			o.logger.Info("orchestrator stop requested")
			o.shutdownWorkers()
			return nil

		case <-ticker.C:
			o.tick(ctx)

		case <-o.refreshCh:
			o.logger.Info("immediate refresh requested")
			o.tick(ctx)
			// Reset the ticker so we get a full interval after the manual refresh.
			ticker.Reset(pollInterval)

		case result := <-o.workerDoneCh:
			o.handleWorkerExit(ctx, result)

		case <-reloadTicker.C:
			o.checkWorkflowReload()
		}
	}
}

// GetState returns a snapshot of the current orchestrator state for the HTTP API.
func (o *Orchestrator) GetState() StateSnapshot {
	o.state.Lock()
	defer o.state.Unlock()

	running := make([]RunningSnapshot, 0, len(o.state.Running))
	for _, entry := range o.state.Running {
		rs := RunningSnapshot{
			IssueID:         entry.Issue.ID,
			IssueIdentifier: entry.Identifier,
			State:           entry.Issue.State,
			SessionID:       entry.Session.SessionID,
			TurnCount:       entry.Session.TurnCount,
			LastEvent:       entry.Session.LastCodexEvent,
			LastMessage:     entry.Session.LastCodexMessage,
			StartedAt:       entry.StartedAt,
			LastEventAt:     entry.Session.LastCodexTimestamp,
			Tokens: domain.TokenUsage{
				InputTokens:  entry.Session.CodexInputTokens,
				OutputTokens: entry.Session.CodexOutputTokens,
				TotalTokens:  entry.Session.CodexTotalTokens,
			},
		}
		running = append(running, rs)
	}

	retrying := make([]RetrySnapshot, 0, len(o.state.RetryAttempts))
	for _, entry := range o.state.RetryAttempts {
		retrying = append(retrying, RetrySnapshot{
			IssueID:         entry.IssueID,
			IssueIdentifier: entry.Identifier,
			Attempt:         entry.Attempt,
			DueAt:           time.UnixMilli(entry.DueAtMs),
			Error:           entry.Error,
		})
	}

	return StateSnapshot{
		GeneratedAt: time.Now(),
		Running:     running,
		Retrying:    retrying,
		CodexTotals: o.state.CodexTotals,
		RateLimits:  o.state.CodexRateLimits,
	}
}

// RequestRefresh triggers an immediate poll cycle (non-blocking).
func (o *Orchestrator) RequestRefresh() {
	select {
	case o.refreshCh <- struct{}{}:
	default:
		// Already pending.
	}
}

// ---------------------------------------------------------------------------
// Startup: terminal cleanup (Section 8.1)
// ---------------------------------------------------------------------------

func (o *Orchestrator) startupTerminalCleanup(ctx context.Context) {
	o.logger.Info("running startup terminal cleanup")
	issues, err := o.tracker.FetchIssuesByStates(o.config.Tracker.TerminalStates)
	if err != nil {
		o.logger.Error("startup terminal cleanup: failed to fetch terminal issues", "error", err)
		return
	}
	for _, issue := range issues {
		o.logger.Info("cleaning workspace for terminal issue", "identifier", issue.Identifier, "state", issue.State)
		o.workspaceMgr.CleanWorkspace(ctx, issue.Identifier)
	}
}

// ---------------------------------------------------------------------------
// Tick: the core poll-dispatch cycle (Sections 8.1-8.3)
// ---------------------------------------------------------------------------

func (o *Orchestrator) tick(ctx context.Context) {
	o.logger.Debug("tick: starting poll cycle")

	// 1. Reconcile running issues first (frees slots before dispatch). Spec Section 8.1.
	o.reconcile(ctx)

	// 2. Validate dispatch config.
	if err := config.ValidateDispatchConfig(o.config); err != nil {
		o.logger.Error("tick: dispatch config validation failed, skipping dispatch", "error", err)
		return
	}

	// 3. Fetch candidates from tracker.
	candidates, err := o.tracker.FetchCandidateIssues()
	if err != nil {
		o.logger.Error("tick: failed to fetch candidate issues", "error", err)
		return
	}

	// 4. Filter eligible candidates.
	eligible := o.filterEligible(candidates)

	// 5. Sort by priority.
	sortIssues(eligible)

	// 6. Dispatch as many as concurrency allows.
	for _, issue := range eligible {
		if ctx.Err() != nil {
			return
		}
		if !o.canDispatch(issue) {
			continue
		}
		o.dispatch(ctx, issue, nil)
	}
}

// ---------------------------------------------------------------------------
// Candidate selection (Section 8.2)
// ---------------------------------------------------------------------------

// filterEligible returns issues that are not already running, claimed,
// completed, or in the retry queue, applying blocker rules for Todo issues.
func (o *Orchestrator) filterEligible(candidates []domain.Issue) []domain.Issue {
	o.state.Lock()
	defer o.state.Unlock()

	eligible := make([]domain.Issue, 0, len(candidates))
	for _, issue := range candidates {
		id := issue.ID

		// Skip if already running, claimed, completed, or retrying.
		if o.state.Running[id] != nil {
			continue
		}
		if o.state.Claimed[id] {
			continue
		}
		if o.state.Completed[id] {
			continue
		}
		if o.state.RetryAttempts[id] != nil {
			continue
		}

		// Blocker rule: "Todo" state issues must not have unresolved blockers.
		if normalizeState(issue.State) == "todo" && hasUnresolvedBlockers(issue, o.config.Tracker.TerminalStates) {
			o.logger.Debug("skipping blocked todo issue", "identifier", issue.Identifier)
			continue
		}

		eligible = append(eligible, issue)
	}
	return eligible
}

// hasUnresolvedBlockers returns true if any blocker is in a non-terminal state.
func hasUnresolvedBlockers(issue domain.Issue, terminalStates []string) bool {
	if len(issue.BlockedBy) == 0 {
		return false
	}
	termSet := make(map[string]bool, len(terminalStates))
	for _, s := range terminalStates {
		termSet[normalizeState(s)] = true
	}
	for _, b := range issue.BlockedBy {
		if b.State == nil {
			// Unknown state counts as unresolved.
			return true
		}
		if !termSet[normalizeState(*b.State)] {
			return true
		}
	}
	return false
}

// normalizeState trims whitespace and lowercases for comparison.
func normalizeState(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// ---------------------------------------------------------------------------
// Issue sorting (priority asc, nil last; created_at oldest first; identifier)
// ---------------------------------------------------------------------------

func sortIssues(issues []domain.Issue) {
	sort.SliceStable(issues, func(i, j int) bool {
		a, b := issues[i], issues[j]

		// Priority ascending, nil sorts last.
		aPri := priorityVal(a.Priority)
		bPri := priorityVal(b.Priority)
		if aPri != bPri {
			return aPri < bPri
		}

		// Created at: oldest first.
		aTime := createdAtVal(a.CreatedAt)
		bTime := createdAtVal(b.CreatedAt)
		if !aTime.Equal(bTime) {
			return aTime.Before(bTime)
		}

		// Identifier lexicographic.
		return a.Identifier < b.Identifier
	})
}

func priorityVal(p *int) int {
	if p == nil {
		return math.MaxInt
	}
	return *p
}

func createdAtVal(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// ---------------------------------------------------------------------------
// Concurrency control (Section 8.3)
// ---------------------------------------------------------------------------

// canDispatch checks global and per-state concurrency limits.
// Must be called WITHOUT the state lock held (it acquires it internally).
func (o *Orchestrator) canDispatch(issue domain.Issue) bool {
	o.state.Lock()
	defer o.state.Unlock()

	// Global limit.
	if len(o.state.Running) >= o.state.MaxConcurrentAgents {
		return false
	}

	// Per-state limit.
	normalized := normalizeState(issue.State)
	limit, hasLimit := o.config.Agent.MaxConcurrentAgentsByState[normalized]
	if hasLimit {
		count := 0
		for _, entry := range o.state.Running {
			if normalizeState(entry.Issue.State) == normalized {
				count++
			}
		}
		if count >= limit {
			return false
		}
	}

	return true
}

// ---------------------------------------------------------------------------
// Dispatch (Section 16.4)
// ---------------------------------------------------------------------------

func (o *Orchestrator) dispatch(ctx context.Context, issue domain.Issue, retryAttempt *int) {
	o.state.Lock()
	// Double-check to prevent duplicate dispatch.
	if o.state.Running[issue.ID] != nil || o.state.Claimed[issue.ID] {
		o.state.Unlock()
		return
	}
	o.state.Claimed[issue.ID] = true
	o.state.Unlock()

	o.logger.Info("dispatching worker",
		"issue_id", issue.ID,
		"identifier", issue.Identifier,
		"state", issue.State,
		"retry_attempt", retryAttempt,
	)

	// Create a cancellable context for the worker.
	workerCtx, workerCancel := context.WithCancel(ctx)

	issueCopy := issue
	entry := &domain.RunningEntry{
		Issue:        &issueCopy,
		Identifier:   issue.Identifier,
		Session:      domain.LiveSession{},
		RetryAttempt: retryAttempt,
		StartedAt:    time.Now(),
		Cancel:       workerCancel,
	}

	o.state.Lock()
	o.state.Running[issue.ID] = entry
	o.state.Unlock()

	attempt := 1
	if retryAttempt != nil {
		attempt = *retryAttempt
	}

	go o.runWorker(workerCtx, &issueCopy, attempt, entry)
}

// ---------------------------------------------------------------------------
// Worker (Section 16.5)
// ---------------------------------------------------------------------------

func (o *Orchestrator) runWorker(ctx context.Context, issue *domain.Issue, attempt int, entry *domain.RunningEntry) {
	var workerErr error

	defer func() {
		o.workerDoneCh <- workerResult{
			issueID:    issue.ID,
			identifier: issue.Identifier,
			attempt:    attempt,
			err:        workerErr,
		}
	}()

	logger := o.logger.With("issue_id", issue.ID, "identifier", issue.Identifier, "attempt", attempt)

	// 1. Create/reuse workspace.
	logger.Info("creating workspace")
	ws, err := o.workspaceMgr.CreateForIssue(ctx, issue.Identifier)
	if err != nil {
		logger.Error("failed to create workspace", "error", err)
		workerErr = fmt.Errorf("create workspace: %w", err)
		return
	}
	logger.Info("workspace ready", "path", ws.Path, "created_now", ws.CreatedNow)

	// 2. Run before_run hook.
	if err := o.workspaceMgr.RunBeforeRun(ctx, ws.Path); err != nil {
		logger.Error("before_run hook failed", "error", err)
		workerErr = fmt.Errorf("before_run hook: %w", err)
		return
	}

	// 3. Build agent runner.
	runnerConfig := agent.RunnerConfig{
		CodexCommand:      o.config.Codex.Command,
		ApprovalPolicy:    o.config.Codex.ApprovalPolicy,
		ThreadSandbox:     o.config.Codex.ThreadSandbox,
		TurnSandboxPolicy: o.config.Codex.TurnSandboxPolicy,
		TurnTimeoutMs:     o.config.Codex.TurnTimeoutMs,
		ReadTimeoutMs:     o.config.Codex.ReadTimeoutMs,
		StallTimeoutMs:    o.config.Codex.StallTimeoutMs,
		MaxTurns:          o.config.Agent.MaxTurns,
	}

	runner := agent.NewDefaultRunner(
		runnerConfig,
		ws.Path,
		o.promptBuilder,
		logger,
	)

	// 4. Build the update callback that feeds CodexUpdate events into orchestrator state.
	onUpdate := func(update domain.CodexUpdate) {
		o.handleCodexUpdate(update)
	}

	// 5. Run the agent session (multi-turn loop is internal to the runner).
	var attemptPtr *int
	if attempt > 0 {
		a := attempt
		attemptPtr = &a
	}

	logger.Info("starting agent session")
	workerErr = runner.RunAttempt(ctx, issue, attemptPtr, onUpdate)
	if workerErr != nil {
		logger.Warn("agent session ended with error", "error", workerErr)
	} else {
		logger.Info("agent session completed normally")
	}

	// 6. Run after_run hook (best-effort).
	o.workspaceMgr.RunAfterRun(ctx, ws.Path)
}

// ---------------------------------------------------------------------------
// Codex update handling (Section 10)
// ---------------------------------------------------------------------------

func (o *Orchestrator) handleCodexUpdate(update domain.CodexUpdate) {
	o.state.Lock()
	defer o.state.Unlock()

	entry, ok := o.state.Running[update.IssueID]
	if !ok {
		return
	}

	now := update.Timestamp
	entry.Session.LastCodexEvent = update.Event
	entry.Session.LastCodexTimestamp = &now
	entry.Session.LastCodexMessage = update.Message

	if update.PID != "" {
		entry.Session.CodexAppServerPID = update.PID
	}

	if update.Usage != nil {
		entry.Session.CodexInputTokens += update.Usage.InputTokens
		entry.Session.CodexOutputTokens += update.Usage.OutputTokens
		entry.Session.CodexTotalTokens += update.Usage.TotalTokens

		o.state.CodexTotals.InputTokens += update.Usage.InputTokens
		o.state.CodexTotals.OutputTokens += update.Usage.OutputTokens
		o.state.CodexTotals.TotalTokens += update.Usage.TotalTokens
	}

	if update.RateLimits != nil {
		o.state.CodexRateLimits = update.RateLimits
	}

	// Track turn completions.
	if update.Event == domain.EventTurnCompleted {
		entry.Session.TurnCount++
	}
}

// ---------------------------------------------------------------------------
// Worker exit handling (Section 16.6)
// ---------------------------------------------------------------------------

func (o *Orchestrator) handleWorkerExit(ctx context.Context, result workerResult) {
	o.logger.Info("worker exited",
		"issue_id", result.issueID,
		"identifier", result.identifier,
		"attempt", result.attempt,
		"error", result.err,
	)

	o.state.Lock()
	entry := o.state.Running[result.issueID]

	// Accumulate runtime.
	if entry != nil {
		elapsed := time.Since(entry.StartedAt).Seconds()
		o.state.CodexTotals.SecondsRunning += elapsed
	}

	delete(o.state.Running, result.issueID)
	delete(o.state.Claimed, result.issueID)
	o.state.Unlock()

	if result.err == nil {
		// Normal completion: mark as completed. The next poll cycle will pick up
		// the issue again only if it's still in an active state (i.e., not moved
		// to Done/Closed by the user).
		o.logger.Info("worker completed successfully, marking done",
			"issue_id", result.issueID,
			"identifier", result.identifier,
		)
		o.state.Lock()
		o.state.Completed[result.issueID] = true
		o.state.Unlock()
		return
	} else {
		// Abnormal exit: exponential backoff.
		nextAttempt := result.attempt + 1
		delay := o.backoffDelay(nextAttempt)
		o.scheduleRetry(result.issueID, result.identifier, nextAttempt, delay, result.err.Error())
	}
}

// backoffDelay computes min(10000 * 2^(attempt-1), max_retry_backoff_ms).
func (o *Orchestrator) backoffDelay(attempt int) int64 {
	base := 10000.0
	delay := base * math.Pow(2, float64(attempt-1))
	maxDelay := float64(o.config.Agent.MaxRetryBackoffMs)
	if delay > maxDelay {
		delay = maxDelay
	}
	return int64(delay)
}

// ---------------------------------------------------------------------------
// Retry handling (Section 8.4)
// ---------------------------------------------------------------------------

func (o *Orchestrator) scheduleRetry(issueID, identifier string, attempt int, delayMs int64, errMsg string) {
	dueAt := time.Now().Add(time.Duration(delayMs) * time.Millisecond)

	o.logger.Info("scheduling retry",
		"issue_id", issueID,
		"identifier", identifier,
		"attempt", attempt,
		"delay_ms", delayMs,
		"due_at", dueAt,
	)

	entry := &domain.RetryEntry{
		IssueID:    issueID,
		Identifier: identifier,
		Attempt:    attempt,
		DueAtMs:    dueAt.UnixMilli(),
		Error:      errMsg,
	}

	// Use a timer that will fire when the retry is due.
	timer := time.NewTimer(time.Duration(delayMs) * time.Millisecond)
	entry.Timer = timer

	o.state.Lock()
	o.state.RetryAttempts[issueID] = entry
	o.state.Unlock()

	go o.waitForRetry(issueID, timer)
}

func (o *Orchestrator) waitForRetry(issueID string, timer *time.Timer) {
	<-timer.C

	o.state.Lock()
	entry, exists := o.state.RetryAttempts[issueID]
	if !exists {
		o.state.Unlock()
		return
	}
	attempt := entry.Attempt
	identifier := entry.Identifier
	delete(o.state.RetryAttempts, issueID)
	o.state.Unlock()

	o.logger.Info("retry timer fired", "issue_id", issueID, "identifier", identifier, "attempt", attempt)

	// Re-fetch the issue from the tracker to get its current state.
	issues, err := o.tracker.FetchIssueStatesByIDs([]string{issueID})
	if err != nil {
		o.logger.Error("retry: failed to fetch issue state", "issue_id", issueID, "error", err)
		// Requeue with same attempt.
		delay := o.backoffDelay(attempt)
		o.scheduleRetry(issueID, identifier, attempt, delay, "fetch failed: "+err.Error())
		return
	}

	if len(issues) == 0 {
		o.logger.Warn("retry: issue not found, marking completed", "issue_id", issueID)
		o.state.Lock()
		o.state.Completed[issueID] = true
		o.state.Unlock()
		return
	}

	issue := issues[0]

	// Check if the issue is now in a terminal state.
	if o.isTerminalState(issue.State) {
		o.logger.Info("retry: issue is now terminal, cleaning up", "issue_id", issueID, "state", issue.State)
		o.state.Lock()
		o.state.Completed[issueID] = true
		o.state.Unlock()
		o.workspaceMgr.CleanWorkspace(context.Background(), identifier)
		return
	}

	// Check if the issue is still in an active state and can be dispatched.
	if !o.isActiveState(issue.State) {
		o.logger.Info("retry: issue no longer in active state, marking completed", "issue_id", issueID, "state", issue.State)
		o.state.Lock()
		o.state.Completed[issueID] = true
		o.state.Unlock()
		return
	}

	// Try to dispatch.
	retryAttempt := attempt
	if o.canDispatch(issue) {
		o.dispatch(context.Background(), issue, &retryAttempt)
	} else {
		// Requeue if cannot dispatch right now.
		o.logger.Info("retry: cannot dispatch, requeuing", "issue_id", issueID)
		o.scheduleRetry(issueID, identifier, attempt, 5000, "concurrency limit reached")
	}
}

// ---------------------------------------------------------------------------
// Reconciliation (Section 8.5)
// ---------------------------------------------------------------------------

func (o *Orchestrator) reconcile(ctx context.Context) {
	o.state.Lock()
	if len(o.state.Running) == 0 {
		o.state.Unlock()
		return
	}

	// Collect running issue IDs and check for stalls.
	issueIDs := make([]string, 0, len(o.state.Running))
	stallTimeout := time.Duration(o.config.Codex.StallTimeoutMs) * time.Millisecond

	for id, entry := range o.state.Running {
		issueIDs = append(issueIDs, id)

		// Stall detection: if last event was too long ago.
		if entry.Session.LastCodexTimestamp != nil && stallTimeout > 0 {
			if time.Since(*entry.Session.LastCodexTimestamp) > stallTimeout {
				o.logger.Warn("stall detected, cancelling worker",
					"issue_id", id,
					"identifier", entry.Identifier,
					"last_event_at", entry.Session.LastCodexTimestamp,
				)
				if entry.Cancel != nil {
					entry.Cancel()
				}
			}
		}
	}
	o.state.Unlock()

	if len(issueIDs) == 0 {
		return
	}

	// Fetch current states from tracker.
	issues, err := o.tracker.FetchIssueStatesByIDs(issueIDs)
	if err != nil {
		o.logger.Error("reconciliation: failed to fetch issue states", "error", err)
		return
	}

	// Build a lookup map.
	stateMap := make(map[string]string, len(issues))
	for _, issue := range issues {
		stateMap[issue.ID] = issue.State
	}

	o.state.Lock()
	defer o.state.Unlock()

	for id, entry := range o.state.Running {
		currentState, found := stateMap[id]
		if !found {
			continue
		}

		// Terminal -> cancel the worker and clean up.
		if o.isTerminalStateLocked(currentState) {
			o.logger.Info("reconciliation: issue moved to terminal state, stopping worker",
				"issue_id", id,
				"identifier", entry.Identifier,
				"state", currentState,
			)
			if entry.Cancel != nil {
				entry.Cancel()
			}
			continue
		}

		// Still active -> update the issue state on the running entry.
		if o.isActiveStateLocked(currentState) {
			entry.Issue.State = currentState
			continue
		}

		// Other (not active, not terminal) -> stop the worker.
		o.logger.Info("reconciliation: issue in unexpected state, stopping worker",
			"issue_id", id,
			"identifier", entry.Identifier,
			"state", currentState,
		)
		if entry.Cancel != nil {
			entry.Cancel()
		}
	}
}

// ---------------------------------------------------------------------------
// Dynamic reload: watch WORKFLOW.md
// ---------------------------------------------------------------------------

func (o *Orchestrator) checkWorkflowReload() {
	fi, err := os.Stat(o.workflowPath)
	if err != nil {
		o.logger.Debug("workflow reload: cannot stat file", "error", err)
		return
	}

	if !fi.ModTime().After(o.workflowMtime) {
		return
	}

	o.logger.Info("workflow file changed, reloading", "path", o.workflowPath)
	o.workflowMtime = fi.ModTime()

	wf, err := config.LoadWorkflow(o.workflowPath)
	if err != nil {
		o.logger.Error("workflow reload: failed to parse", "error", err)
		return
	}

	newCfg := config.ParseServiceConfig(wf.Config)

	// Preserve server port override.
	if o.config.Server.Port != nil {
		newCfg.Server.Port = o.config.Server.Port
	}

	if err := config.ValidateDispatchConfig(newCfg); err != nil {
		o.logger.Error("workflow reload: new config invalid, keeping old", "error", err)
		return
	}

	// Apply the new config.
	o.config = newCfg
	o.workflow = wf

	// Update dependent components.
	o.promptBuilder.UpdateTemplate(wf.PromptTemplate)
	o.workspaceMgr.UpdateConfig(newCfg.Workspace.Root, workspace.HooksConfig{
		AfterCreate:  newCfg.Hooks.AfterCreate,
		BeforeRun:    newCfg.Hooks.BeforeRun,
		AfterRun:     newCfg.Hooks.AfterRun,
		BeforeRemove: newCfg.Hooks.BeforeRemove,
		TimeoutMs:    newCfg.Hooks.TimeoutMs,
	})

	// Update state limits.
	o.state.Lock()
	o.state.PollIntervalMs = newCfg.Polling.IntervalMs
	o.state.MaxConcurrentAgents = newCfg.Agent.MaxConcurrentAgents
	o.state.Unlock()

	o.logger.Info("workflow reload complete",
		"poll_interval_ms", newCfg.Polling.IntervalMs,
		"max_concurrent", newCfg.Agent.MaxConcurrentAgents,
	)
}

// ---------------------------------------------------------------------------
// HTTP Server
// ---------------------------------------------------------------------------

func (o *Orchestrator) startHTTPServer() {
	port := o.config.Server.Port
	if port == nil {
		return
	}

	srv := server.New(o, *port)
	addr, err := srv.Start()
	if err != nil {
		o.logger.Error("failed to start HTTP server", "error", err)
		return
	}
	o.logger.Info("HTTP server listening", "addr", addr)
}

// ---------------------------------------------------------------------------
// Shutdown
// ---------------------------------------------------------------------------

func (o *Orchestrator) shutdownWorkers() {
	o.state.Lock()
	defer o.state.Unlock()

	for id, entry := range o.state.Running {
		o.logger.Info("cancelling worker during shutdown", "issue_id", id)
		if entry.Cancel != nil {
			entry.Cancel()
		}
	}

	// Stop pending retry timers.
	for id, entry := range o.state.RetryAttempts {
		if entry.Timer != nil {
			entry.Timer.Stop()
		}
		delete(o.state.RetryAttempts, id)
	}
}

// ---------------------------------------------------------------------------
// State helpers (some require lock held, some acquire it)
// ---------------------------------------------------------------------------

func (o *Orchestrator) isTerminalState(state string) bool {
	normalized := normalizeState(state)
	for _, ts := range o.config.Tracker.TerminalStates {
		if normalizeState(ts) == normalized {
			return true
		}
	}
	return false
}

func (o *Orchestrator) isActiveState(state string) bool {
	normalized := normalizeState(state)
	for _, as := range o.config.Tracker.ActiveStates {
		if normalizeState(as) == normalized {
			return true
		}
	}
	return false
}

// isTerminalStateLocked is for use when the caller already holds the state lock.
// It reads only from o.config which is safe.
func (o *Orchestrator) isTerminalStateLocked(state string) bool {
	return o.isTerminalState(state)
}

// isActiveStateLocked is for use when the caller already holds the state lock.
func (o *Orchestrator) isActiveStateLocked(state string) bool {
	return o.isActiveState(state)
}

// ---------------------------------------------------------------------------
// Ensure interface compliance (compile-time check).
// ---------------------------------------------------------------------------

var _ sync.Locker = (*domain.OrchestratorState)(nil)
