package orchestrator

import (
	"fmt"
	"time"

	"github.com/monlabs/symphony/internal/server"
)

// Ensure Orchestrator implements server.StateProvider at compile time.
var _ server.StateProvider = (*Orchestrator)(nil)

// GetStateSnapshot implements server.StateProvider.
func (o *Orchestrator) GetStateSnapshot() server.StateSnapshot {
	snap := o.GetState()
	now := time.Now().UTC()

	running := make([]server.RunningRow, len(snap.Running))
	for i, r := range snap.Running {
		row := server.RunningRow{
			IssueID:         r.IssueID,
			IssueIdentifier: r.IssueIdentifier,
			State:           r.State,
			SessionID:       r.SessionID,
			TurnCount:       r.TurnCount,
			LastEvent:       r.LastEvent,
			LastMessage:     r.LastMessage,
			StartedAt:       r.StartedAt.Format(time.RFC3339),
			Tokens: server.TokensJSON{
				InputTokens:  r.Tokens.InputTokens,
				OutputTokens: r.Tokens.OutputTokens,
				TotalTokens:  r.Tokens.TotalTokens,
			},
		}
		if r.LastEventAt != nil {
			t := r.LastEventAt.Format(time.RFC3339)
			row.LastEventAt = &t
		}
		running[i] = row
	}

	retrying := make([]server.RetryRow, len(snap.Retrying))
	for i, r := range snap.Retrying {
		retrying[i] = server.RetryRow{
			IssueID:         r.IssueID,
			IssueIdentifier: r.IssueIdentifier,
			Attempt:         r.Attempt,
			DueAt:           r.DueAt.Format(time.RFC3339),
			Error:           r.Error,
		}
	}

	// Compute live runtime
	liveRuntime := snap.CodexTotals.SecondsRunning
	for _, r := range snap.Running {
		liveRuntime += now.Sub(r.StartedAt).Seconds()
	}

	return server.StateSnapshot{
		GeneratedAt: now.Format(time.RFC3339),
		Counts: server.SnapshotCounts{
			Running:  len(running),
			Retrying: len(retrying),
		},
		Running:  running,
		Retrying: retrying,
		CodexTotals: server.CodexTotalsJSON{
			InputTokens:    snap.CodexTotals.InputTokens,
			OutputTokens:   snap.CodexTotals.OutputTokens,
			TotalTokens:    snap.CodexTotals.TotalTokens,
			SecondsRunning: liveRuntime,
		},
		RateLimits: snap.RateLimits,
	}
}

// GetIssueDetail implements server.StateProvider.
func (o *Orchestrator) GetIssueDetail(identifier string) (*server.IssueDetail, error) {
	o.state.Lock()
	defer o.state.Unlock()

	// Search running
	for _, entry := range o.state.Running {
		if entry.Identifier == identifier {
			wsPath := o.workspaceMgr.WorkspacePath(identifier)
			row := server.RunningRow{
				IssueID:         entry.Issue.ID,
				IssueIdentifier: entry.Identifier,
				State:           entry.Issue.State,
				SessionID:       entry.Session.SessionID,
				TurnCount:       entry.Session.TurnCount,
				LastEvent:       entry.Session.LastCodexEvent,
				LastMessage:     entry.Session.LastCodexMessage,
				StartedAt:       entry.StartedAt.Format(time.RFC3339),
				Tokens: server.TokensJSON{
					InputTokens:  entry.Session.CodexInputTokens,
					OutputTokens: entry.Session.CodexOutputTokens,
					TotalTokens:  entry.Session.CodexTotalTokens,
				},
			}
			if entry.Session.LastCodexTimestamp != nil {
				t := entry.Session.LastCodexTimestamp.Format(time.RFC3339)
				row.LastEventAt = &t
			}
			return &server.IssueDetail{
				IssueIdentifier: identifier,
				IssueID:         entry.Issue.ID,
				Status:          "running",
				Workspace:       &server.WsInfo{Path: wsPath},
				Running:         &row,
			}, nil
		}
	}

	// Search retry queue
	for _, entry := range o.state.RetryAttempts {
		if entry.Identifier == identifier {
			row := server.RetryRow{
				IssueID:         entry.IssueID,
				IssueIdentifier: entry.Identifier,
				Attempt:         entry.Attempt,
				DueAt:           time.UnixMilli(entry.DueAtMs).Format(time.RFC3339),
				Error:           entry.Error,
			}
			return &server.IssueDetail{
				IssueIdentifier: identifier,
				IssueID:         entry.IssueID,
				Status:          "retrying",
				Retry:           &row,
				LastError:       &entry.Error,
			}, nil
		}
	}

	return nil, fmt.Errorf("issue %q not found in current state", identifier)
}
