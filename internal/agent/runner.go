package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/monlabs/symphony/internal/domain"
)

const (
	maxScannerBuf = 10 * 1024 * 1024 // 10 MB max line size for stdout scanner
)

// RunnerConfig holds configuration for the agent runner subprocess.
type RunnerConfig struct {
	CodexCommand      string
	ApprovalPolicy    interface{} // string or map[string]interface{}
	ThreadSandbox     string
	TurnSandboxPolicy string
	LinearAPIKey      string

	// IssueStateChecker is called between turns to check if the issue is still active.
	// Returns (stillActive bool, err error). If nil, turns continue unconditionally.
	IssueStateChecker func(issueID string) (bool, error)
	TurnTimeoutMs     int
	ReadTimeoutMs     int
	StallTimeoutMs    int
	MaxTurns          int
}

// UpdateCallback is called to forward CodexUpdate events to the orchestrator.
type UpdateCallback func(update domain.CodexUpdate)

// Runner defines the interface for running a coding agent attempt against an issue.
type Runner interface {
	// RunAttempt runs a full agent attempt for an issue.
	// It handles subprocess lifecycle, session initialization, and the multi-turn loop.
	RunAttempt(ctx context.Context, issue *domain.Issue, attempt *int, onUpdate UpdateCallback) error
}

// PromptBuilder builds the rendered prompt string for an issue attempt.
type PromptBuilder interface {
	BuildPrompt(issue *domain.Issue, attempt *int) (string, error)
}

// DefaultRunner implements Runner using an app-server subprocess.
type DefaultRunner struct {
	config        RunnerConfig
	workspacePath string
	promptBuilder PromptBuilder
	logger        *slog.Logger
}

// NewDefaultRunner creates a new DefaultRunner.
func NewDefaultRunner(config RunnerConfig, workspacePath string, promptBuilder PromptBuilder, logger *slog.Logger) *DefaultRunner {
	if logger == nil {
		logger = slog.Default()
	}
	return &DefaultRunner{
		config:        config,
		workspacePath: workspacePath,
		promptBuilder: promptBuilder,
		logger:        logger,
	}
}

// RunAttempt implements Runner.RunAttempt.
func (r *DefaultRunner) RunAttempt(ctx context.Context, issue *domain.Issue, attempt *int, onUpdate UpdateCallback) error {
	r.logger.Info("starting agent attempt",
		"issue_id", issue.ID,
		"identifier", issue.Identifier,
		"attempt", attempt,
		"workspace", r.workspacePath,
	)

	// Build the prompt for the first turn.
	prompt, err := r.promptBuilder.BuildPrompt(issue, attempt)
	if err != nil {
		r.emitUpdate(onUpdate, issue.ID, domain.EventStartupFailed, "failed to build prompt: "+err.Error(), nil)
		return fmt.Errorf("build prompt: %w", err)
	}

	// Start the app-server subprocess.
	cmd := exec.CommandContext(ctx, "bash", "-lc", r.config.CodexCommand)
	cmd.Dir = r.workspacePath

	stdin, err := cmd.StdinPipe()
	if err != nil {
		r.emitUpdate(onUpdate, issue.ID, domain.EventStartupFailed, "failed to create stdin pipe: "+err.Error(), nil)
		return fmt.Errorf("stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		r.emitUpdate(onUpdate, issue.ID, domain.EventStartupFailed, "failed to create stdout pipe: "+err.Error(), nil)
		return fmt.Errorf("stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		r.emitUpdate(onUpdate, issue.ID, domain.EventStartupFailed, "failed to create stderr pipe: "+err.Error(), nil)
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		r.emitUpdate(onUpdate, issue.ID, domain.EventStartupFailed, "failed to start subprocess: "+err.Error(), nil)
		return fmt.Errorf("start subprocess: %w", err)
	}

	pid := strconv.Itoa(cmd.Process.Pid)
	r.logger.Info("subprocess started", "pid", pid, "issue_id", issue.ID)

	// Drain stderr in background for diagnostics.
	go r.drainStderr(stderr, issue.ID)

	// Create the session to manage communication.
	sess := &session{
		stdin:         stdin,
		stdout:        stdout,
		logger:        r.logger,
		issueID:       issue.ID,
		pid:           pid,
		readTimeoutMs: r.config.ReadTimeoutMs,
		onUpdate:      onUpdate,
		pending:       make(map[int64]chan *ProtocolMessage),
	}

	// Start the reader goroutine.
	sess.startReader()

	// Ensure cleanup.
	defer func() {
		stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		sess.close()
		r.logger.Info("subprocess stopped", "pid", pid, "issue_id", issue.ID)
	}()

	// 1. Send initialize request.
	initResult, err := sess.sendRequest(ctx, "initialize", map[string]interface{}{
		"protocolVersion": "2025-01-01",
		"capabilities":    map[string]interface{}{
			"experimentalApi": true,
		},
		"clientInfo": map[string]interface{}{
			"name":    "symphony",
			"version": "0.1.0",
		},
	})
	if err != nil {
		r.emitUpdate(onUpdate, issue.ID, domain.EventStartupFailed, "initialize failed: "+err.Error(), nil)
		return fmt.Errorf("initialize: %w", err)
	}
	r.logger.Info("initialize response received", "issue_id", issue.ID, "result", initResult)

	// 2. Send initialized notification.
	if err := sess.sendNotification("initialized", nil); err != nil {
		r.emitUpdate(onUpdate, issue.ID, domain.EventStartupFailed, "initialized notification failed: "+err.Error(), nil)
		return fmt.Errorf("send initialized: %w", err)
	}

	// 3. Send thread/start request (camelCase per Codex app-server schema).
	threadStartParams := map[string]interface{}{
		"sandbox":      r.config.ThreadSandbox,
		"cwd":          r.workspacePath,
		"dynamicTools": LinearGraphQLToolSpecs(),
	}
	if r.config.ApprovalPolicy != nil && r.config.ApprovalPolicy != "" {
		threadStartParams["approvalPolicy"] = r.config.ApprovalPolicy
	}
	threadResult, err := sess.sendRequest(ctx, "thread/start", threadStartParams)
	if err != nil {
		r.emitUpdate(onUpdate, issue.ID, domain.EventStartupFailed, "thread/start failed: "+err.Error(), nil)
		return fmt.Errorf("thread/start: %w", err)
	}

	// Spec Section 10.2: read thread_id from result.thread.id (nested) or flat thread_id.
	threadID := extractThreadID(threadResult)
	if threadID == "" {
		r.emitUpdate(onUpdate, issue.ID, domain.EventStartupFailed, "thread/start returned no thread_id", nil)
		return fmt.Errorf("thread/start: missing thread_id in response")
	}
	r.logger.Info("thread started", "thread_id", threadID, "issue_id", issue.ID)

	r.emitUpdate(onUpdate, issue.ID, domain.EventSessionStarted, "session started", nil)

	// 4. Turn loop.
	for turn := 1; turn <= r.config.MaxTurns; turn++ {
		turnID := fmt.Sprintf("%s-turn-%d", threadID, turn)
		sessionID := fmt.Sprintf("%s-%s", threadID, turnID)

		r.logger.Info("starting turn", "turn", turn, "turn_id", turnID, "session_id", sessionID, "issue_id", issue.ID)

		// Build turn prompt: first turn uses the built prompt, subsequent turns use continuation guidance.
		var turnPrompt string
		if turn == 1 {
			turnPrompt = prompt
		} else {
			turnPrompt = fmt.Sprintf(`Continuation guidance:

- The previous Codex turn completed normally, but the Linear issue is still in an active state.
- This is continuation turn #%d of %d for the current agent run.
- Resume from the current workspace and workpad state instead of restarting from scratch.
- The original task instructions and prior turn context are already present in this thread, so do not restate them before acting.
- Focus on the remaining ticket work and do not end the turn while the issue stays active unless you are truly blocked.`, turn, r.config.MaxTurns)
		}

		// Create per-turn context with timeout.
		var turnCtx context.Context
		var turnCancel context.CancelFunc
		if r.config.TurnTimeoutMs > 0 {
			turnCtx, turnCancel = context.WithTimeout(ctx, time.Duration(r.config.TurnTimeoutMs)*time.Millisecond)
		} else {
			turnCtx, turnCancel = context.WithCancel(ctx)
		}

		// Send turn/start request (camelCase per Codex app-server schema).
		// input is an array of UserInput objects, not a plain string.
		turnParams := map[string]interface{}{
			"threadId": threadID,
			"cwd":      r.workspacePath,
			"title":    fmt.Sprintf("%s: %s", issue.Identifier, issue.Title),
			"input": []map[string]interface{}{
				{
					"type": "text",
					"text": turnPrompt,
				},
			},
		}
		if r.config.ApprovalPolicy != nil && r.config.ApprovalPolicy != "" {
			turnParams["approvalPolicy"] = r.config.ApprovalPolicy
		}
		if r.config.TurnSandboxPolicy != "" {
			turnParams["sandboxPolicy"] = buildSandboxPolicy(r.config.TurnSandboxPolicy)
		}

		_, err := sess.sendRequest(turnCtx, "turn/start", turnParams)
		if err != nil {
			turnCancel()
			r.emitUpdate(onUpdate, issue.ID, domain.EventTurnFailed, fmt.Sprintf("turn/start failed on turn %d: %s", turn, err.Error()), nil)
			return fmt.Errorf("turn/start (turn %d): %w", turn, err)
		}

		// Process events for this turn.
		outcome, err := r.processTurnEvents(turnCtx, sess, issue.ID, threadID, turn, onUpdate)
		turnCancel()

		if err != nil {
			return fmt.Errorf("turn %d event processing: %w", turn, err)
		}

		switch outcome {
		case turnOutcomeCompleted:
			r.logger.Info("turn completed successfully", "turn", turn, "issue_id", issue.ID)
			r.emitUpdate(onUpdate, issue.ID, domain.EventTurnCompleted, fmt.Sprintf("turn %d completed", turn), nil)

			// Re-check if issue is still in an active state before continuing.
			// This matches the Elixir reference: continue_with_issue?()
			if r.config.IssueStateChecker != nil {
				stillActive, err := r.config.IssueStateChecker(issue.ID)
				if err != nil {
					r.logger.Warn("failed to check issue state between turns, stopping", "error", err, "issue_id", issue.ID)
					return nil // Graceful stop, orchestrator will re-check
				}
				if !stillActive {
					r.logger.Info("issue no longer in active state after turn, stopping", "turn", turn, "issue_id", issue.ID)
					return nil
				}
				r.logger.Info("issue still active, continuing to next turn", "turn", turn+1, "issue_id", issue.ID)
			}
			continue

		case turnOutcomeFailed:
			r.logger.Warn("turn failed", "turn", turn, "issue_id", issue.ID)
			return fmt.Errorf("turn %d failed", turn)

		case turnOutcomeCancelled:
			r.logger.Info("turn cancelled", "turn", turn, "issue_id", issue.ID)
			return fmt.Errorf("turn %d cancelled", turn)

		case turnOutcomeDone:
			// Agent indicated the task is complete.
			r.logger.Info("agent signaled task completion", "turn", turn, "issue_id", issue.ID)
			return nil
		}
	}

	r.logger.Warn("max turns reached", "max_turns", r.config.MaxTurns, "issue_id", issue.ID)
	return fmt.Errorf("max turns (%d) reached without completion", r.config.MaxTurns)
}

type turnOutcome int

const (
	turnOutcomeCompleted  turnOutcome = iota // turn finished, may need more turns
	turnOutcomeFailed                        // turn failed
	turnOutcomeCancelled                     // turn was cancelled
	turnOutcomeDone                          // agent says task is done
)

// processTurnEvents reads and handles events for a single turn until the turn ends.
func (r *DefaultRunner) processTurnEvents(ctx context.Context, sess *session, issueID, threadID string, turn int, onUpdate UpdateCallback) (turnOutcome, error) {
	stallTimeout := time.Duration(r.config.StallTimeoutMs) * time.Millisecond
	if stallTimeout == 0 {
		stallTimeout = 120 * time.Second // default 2 min stall timeout
	}

	for {
		msg, err := sess.readEvent(ctx, stallTimeout)
		if err != nil {
			if ctx.Err() != nil {
				r.emitUpdate(onUpdate, issueID, domain.EventTurnFailed, "turn timed out", nil)
				return turnOutcomeFailed, fmt.Errorf("context expired: %w", ctx.Err())
			}
			r.emitUpdate(onUpdate, issueID, domain.EventTurnFailed, "read event error: "+err.Error(), nil)
			return turnOutcomeFailed, fmt.Errorf("read event: %w", err)
		}

		if msg == nil {
			// Stall detected - no message within stall timeout.
			r.emitUpdate(onUpdate, issueID, domain.EventTurnFailed, "stall detected: no messages received", nil)
			return turnOutcomeFailed, fmt.Errorf("stall timeout exceeded")
		}

		// Handle the message based on its method.
		switch msg.Method {
		case "turn/completed":
			r.extractAndForwardUsage(msg, onUpdate, issueID)
			return turnOutcomeCompleted, nil

		case "turn/done":
			r.extractAndForwardUsage(msg, onUpdate, issueID)
			return turnOutcomeDone, nil

		case "turn/failed":
			errMsg := "turn failed"
			if msg.Params != nil {
				if reason, ok := msg.Params["reason"].(string); ok {
					errMsg = "turn failed: " + reason
				}
			}
			r.emitUpdate(onUpdate, issueID, domain.EventTurnFailed, errMsg, nil)
			return turnOutcomeFailed, nil

		case "turn/cancelled":
			r.emitUpdate(onUpdate, issueID, domain.EventTurnCancelled, "turn cancelled by server", nil)
			return turnOutcomeCancelled, nil

		case "item/commandExecution/requestApproval",
			"execCommandApproval",
			"applyPatchApproval",
			"item/fileChange/requestApproval":
			// Auto-approve command execution and file change approvals.
			r.handleApproval(ctx, sess, msg, issueID, onUpdate)

		case "item/tool/requestUserInput":
			// User input requests: reply with non-interactive answer.
			r.handleToolRequestUserInput(ctx, sess, msg, issueID, onUpdate)

		case "item/tool/call":
			// Dynamic tool call: execute via dynamic tool handler.
			r.handleToolCall(ctx, sess, msg, issueID, onUpdate)

		case "notification":
			// Forward notification events.
			message := ""
			if msg.Params != nil {
				message, _ = msg.Params["message"].(string)
			}
			r.emitUpdate(onUpdate, issueID, domain.EventNotification, message, nil)

		case "rate_limit":
			// Forward rate limit info.
			if msg.Params != nil {
				r.emitUpdate(onUpdate, issueID, domain.EventOtherMessage, "rate limit event", nil)
				// Forward rate limit data via update.
				onUpdate(domain.CodexUpdate{
					IssueID:    issueID,
					Event:      domain.EventOtherMessage,
					Timestamp:  time.Now(),
					PID:        sess.pid,
					Message:    "rate_limit",
					RateLimits: msg.Params,
				})
			}

		default:
			// Extract usage from any message that carries it.
			r.extractAndForwardUsage(msg, onUpdate, issueID)

			if msg.Method != "" {
				r.logger.Debug("unhandled event method", "method", msg.Method, "issue_id", issueID)
				r.emitUpdate(onUpdate, issueID, domain.EventOtherMessage, "method: "+msg.Method, nil)
			}
		}
	}
}

// handleApproval auto-approves approval requests from the app-server.
// Uses "acceptForSession" decision to match Codex app-server protocol.
func (r *DefaultRunner) handleApproval(ctx context.Context, sess *session, msg *ProtocolMessage, issueID string, onUpdate UpdateCallback) {
	r.logger.Info("auto-approving request", "method", msg.Method, "issue_id", issueID)
	r.emitUpdate(onUpdate, issueID, domain.EventApprovalAutoApproved, fmt.Sprintf("auto-approved: %s", msg.Method), nil)

	if msg.ID != nil {
		resp := &ProtocolMessage{
			ID:     msg.ID,
			Result: map[string]interface{}{"decision": "acceptForSession"},
		}
		if err := sess.writeMessage(resp); err != nil {
			r.logger.Error("failed to send approval response", "error", err, "issue_id", issueID)
		}
	}
}

// handleToolRequestUserInput replies with a non-interactive answer.
func (r *DefaultRunner) handleToolRequestUserInput(ctx context.Context, sess *session, msg *ProtocolMessage, issueID string, onUpdate UpdateCallback) {
	r.logger.Info("tool user input requested, sending non-interactive answer", "issue_id", issueID)
	r.emitUpdate(onUpdate, issueID, domain.EventTurnInputRequired, "user input required, sending non-interactive answer", nil)

	if msg.ID != nil {
		// Build answers map from questions if present.
		answers := map[string]interface{}{}
		if msg.Params != nil {
			if questions, ok := msg.Params["questions"].([]interface{}); ok {
				for _, q := range questions {
					if qMap, ok := q.(map[string]interface{}); ok {
						if qID, ok := qMap["id"].(string); ok {
							answers[qID] = map[string]interface{}{
								"answers": []string{"This is a non-interactive session. Operator input is unavailable."},
							}
						}
					}
				}
			}
		}

		resp := &ProtocolMessage{
			ID:     msg.ID,
			Result: map[string]interface{}{"answers": answers},
		}
		if err := sess.writeMessage(resp); err != nil {
			r.logger.Error("failed to send input answer response", "error", err, "issue_id", issueID)
		}
	}
}

// handleToolCall executes dynamic tool calls from Codex (e.g. linear_graphql).
func (r *DefaultRunner) handleToolCall(ctx context.Context, sess *session, msg *ProtocolMessage, issueID string, onUpdate UpdateCallback) {
	toolName := ""
	var arguments interface{}
	if msg.Params != nil {
		// Try "tool" then "name" for the tool name.
		if t, ok := msg.Params["tool"].(string); ok {
			toolName = t
		} else if n, ok := msg.Params["name"].(string); ok {
			toolName = n
		}
		arguments = msg.Params["arguments"]
	}

	r.logger.Info("executing dynamic tool call", "tool", toolName, "issue_id", issueID)

	result := ExecuteDynamicTool(toolName, arguments, r.config.LinearAPIKey)

	success, _ := result["success"].(bool)
	if success {
		r.emitUpdate(onUpdate, issueID, domain.EventOtherMessage, fmt.Sprintf("tool call completed: %s", toolName), nil)
	} else {
		r.emitUpdate(onUpdate, issueID, domain.EventUnsupportedToolCall, fmt.Sprintf("tool call failed: %s", toolName), nil)
	}

	if msg.ID != nil {
		resp := &ProtocolMessage{
			ID:     msg.ID,
			Result: result,
		}
		if err := sess.writeMessage(resp); err != nil {
			r.logger.Error("failed to send tool call response", "error", err, "issue_id", issueID)
		}
	}
}

// extractAndForwardUsage extracts token usage from a message and forwards it.
func (r *DefaultRunner) extractAndForwardUsage(msg *ProtocolMessage, onUpdate UpdateCallback, issueID string) {
	if msg.Params == nil {
		return
	}

	usageRaw, ok := msg.Params["usage"]
	if !ok {
		return
	}

	usageMap, ok := usageRaw.(map[string]interface{})
	if !ok {
		return
	}

	usage := &domain.TokenUsage{
		InputTokens:  toInt64(usageMap["input_tokens"]),
		OutputTokens: toInt64(usageMap["output_tokens"]),
		TotalTokens:  toInt64(usageMap["total_tokens"]),
	}

	if usage.InputTokens > 0 || usage.OutputTokens > 0 || usage.TotalTokens > 0 {
		r.emitUpdate(onUpdate, issueID, domain.EventOtherMessage, "token usage update", usage)
	}
}

// emitUpdate sends a CodexUpdate via the callback.
func (r *DefaultRunner) emitUpdate(onUpdate UpdateCallback, issueID, event, message string, usage *domain.TokenUsage) {
	if onUpdate == nil {
		return
	}
	onUpdate(domain.CodexUpdate{
		IssueID:   issueID,
		Event:     event,
		Timestamp: time.Now(),
		Message:   message,
		Usage:     usage,
	})
}

// drainStderr reads stderr and logs lines as diagnostics.
func (r *DefaultRunner) drainStderr(stderr io.ReadCloser, issueID string) {
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		r.logger.Debug("subprocess stderr", "line", scanner.Text(), "issue_id", issueID)
	}
	if err := scanner.Err(); err != nil {
		r.logger.Debug("stderr scanner error", "error", err, "issue_id", issueID)
	}
}

// session manages the JSON-RPC communication with the app-server subprocess.
type session struct {
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	logger  *slog.Logger
	issueID string
	pid     string

	readTimeoutMs int
	onUpdate      UpdateCallback

	// Request ID counter for outgoing requests.
	requestID atomic.Int64

	// Pending responses keyed by request ID.
	mu      sync.Mutex
	pending map[int64]chan *ProtocolMessage

	// Channel for incoming events (notifications and requests from server).
	events     chan *ProtocolMessage
	readerDone chan struct{}
}

// startReader begins reading line-delimited JSON from stdout in a goroutine.
func (s *session) startReader() {
	s.events = make(chan *ProtocolMessage, 256)
	s.readerDone = make(chan struct{})

	go func() {
		defer close(s.readerDone)
		defer close(s.events)

		scanner := bufio.NewScanner(s.stdout)
		buf := make([]byte, 0, 64*1024)
		scanner.Buffer(buf, maxScannerBuf)

		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}

			var msg ProtocolMessage
			if err := json.Unmarshal(line, &msg); err != nil {
				s.logger.Debug("failed to parse stdout line as JSON",
					"error", err,
					"line", string(line),
					"issue_id", s.issueID,
				)
				// Forward as a malformed event.
				select {
				case s.events <- &ProtocolMessage{
					Method: "malformed",
					Params: map[string]interface{}{"raw": string(line)},
				}:
				default:
				}
				continue
			}

			// If this is a response to a pending request, route it there.
			if msg.IsResponse() {
				id := toInt64(msg.ID)
				s.mu.Lock()
				ch, ok := s.pending[id]
				if ok {
					delete(s.pending, id)
				}
				s.mu.Unlock()

				if ok {
					ch <- &msg
					close(ch)
				} else {
					s.logger.Debug("received response for unknown request ID",
						"id", msg.ID,
						"issue_id", s.issueID,
					)
				}
				continue
			}

			// Otherwise it's a notification or server request - send to events channel.
			select {
			case s.events <- &msg:
			default:
				s.logger.Warn("event channel full, dropping message",
					"method", msg.Method,
					"issue_id", s.issueID,
				)
			}
		}

		if err := scanner.Err(); err != nil {
			s.logger.Debug("stdout scanner error", "error", err, "issue_id", s.issueID)
		}
	}()
}

// sendRequest sends a JSON-RPC request and waits for the matching response.
func (s *session) sendRequest(ctx context.Context, method string, params map[string]interface{}) (map[string]interface{}, error) {
	id := s.requestID.Add(1)

	// Register pending response channel.
	ch := make(chan *ProtocolMessage, 1)
	s.mu.Lock()
	s.pending[id] = ch
	s.mu.Unlock()

	msg := &ProtocolMessage{
		ID:     id,
		Method: method,
		Params: params,
	}

	if err := s.writeMessage(msg); err != nil {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, fmt.Errorf("write request: %w", err)
	}

	// Wait for response with timeout.
	readTimeout := time.Duration(s.readTimeoutMs) * time.Millisecond
	if readTimeout == 0 {
		readTimeout = 30 * time.Second
	}

	timer := time.NewTimer(readTimeout)
	defer timer.Stop()

	select {
	case resp, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("response channel closed for %s (id=%d)", method, id)
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("server error for %s: [%d] %s", method, resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil

	case <-timer.C:
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, fmt.Errorf("read timeout waiting for response to %s (id=%d)", method, id)

	case <-ctx.Done():
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, fmt.Errorf("context cancelled waiting for response to %s: %w", method, ctx.Err())
	}
}

// sendNotification sends a JSON-RPC notification (no ID, no response expected).
func (s *session) sendNotification(method string, params map[string]interface{}) error {
	msg := &ProtocolMessage{
		Method: method,
		Params: params,
	}
	return s.writeMessage(msg)
}

// writeMessage marshals and writes a ProtocolMessage as a JSON line to stdin.
func (s *session) writeMessage(msg *ProtocolMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}

	data = append(data, '\n')

	_, err = s.stdin.Write(data)
	if err != nil {
		return fmt.Errorf("write to stdin: %w", err)
	}

	s.logger.Debug("sent message", "data", string(data[:len(data)-1]), "issue_id", s.issueID)
	return nil
}

// readEvent reads the next event from the events channel with a stall timeout.
// Returns nil message on stall timeout (no messages within stallTimeout).
func (s *session) readEvent(ctx context.Context, stallTimeout time.Duration) (*ProtocolMessage, error) {
	timer := time.NewTimer(stallTimeout)
	defer timer.Stop()

	select {
	case msg, ok := <-s.events:
		if !ok {
			return nil, fmt.Errorf("event channel closed (subprocess exited)")
		}
		return msg, nil

	case <-timer.C:
		return nil, nil // stall

	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// close shuts down the session, draining any remaining pending requests.
func (s *session) close() {
	s.mu.Lock()
	for id, ch := range s.pending {
		close(ch)
		delete(s.pending, id)
	}
	s.mu.Unlock()
}

// buildSandboxPolicy converts a sandbox policy string to the structured object
// expected by the Codex app-server protocol.
func buildSandboxPolicy(policy string) map[string]interface{} {
	switch policy {
	case "danger-full-access":
		return map[string]interface{}{"type": "dangerFullAccess"}
	case "read-only":
		return map[string]interface{}{"type": "readOnly"}
	case "workspace-write":
		return map[string]interface{}{"type": "workspaceWrite"}
	default:
		return map[string]interface{}{"type": "workspaceWrite"}
	}
}

// extractThreadID reads thread_id from a thread/start response.
// Tries nested result.thread.id first (spec), then flat thread_id for compatibility.
func extractThreadID(result map[string]interface{}) string {
	// Nested: result.thread.id (per spec Section 10.2)
	if thread, ok := result["thread"].(map[string]interface{}); ok {
		if id, ok := thread["id"].(string); ok && id != "" {
			return id
		}
	}
	// Flat fallback: result.thread_id
	if id, ok := result["thread_id"].(string); ok && id != "" {
		return id
	}
	// Also try result.threadId (camelCase variant)
	if id, ok := result["threadId"].(string); ok && id != "" {
		return id
	}
	return ""
}

// toInt64 converts various numeric types to int64.
func toInt64(v interface{}) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case float32:
		return int64(n)
	case int:
		return int64(n)
	case int64:
		return n
	case int32:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	case string:
		i, _ := strconv.ParseInt(n, 10, 64)
		return i
	default:
		return 0
	}
}
