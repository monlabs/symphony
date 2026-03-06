package orchestrator

import (
	"log/slog"
	"math"
	"testing"
	"time"

	"github.com/monlabs/symphony/internal/config"
	"github.com/monlabs/symphony/internal/domain"
)

// ---------------------------------------------------------------------------
// Mock tracker
// ---------------------------------------------------------------------------

type mockTracker struct {
	candidates    []domain.Issue
	candidateErr  error
	statesByID    []domain.Issue
	statesByIDErr error
	byStates      []domain.Issue
	byStatesErr   error
}

func (m *mockTracker) FetchCandidateIssues() ([]domain.Issue, error) {
	return m.candidates, m.candidateErr
}

func (m *mockTracker) FetchIssuesByStates(_ []string) ([]domain.Issue, error) {
	return m.byStates, m.byStatesErr
}

func (m *mockTracker) FetchIssueStatesByIDs(_ []string) ([]domain.Issue, error) {
	return m.statesByID, m.statesByIDErr
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func intPtr(v int) *int       { return &v }
func strPtr(v string) *string { return &v }
func timePtr(t time.Time) *time.Time { return &t }

// newTestOrchestrator creates a minimal Orchestrator suitable for unit tests.
func newTestOrchestrator(maxConcurrent int, terminalStates, activeStates []string, byState map[string]int) *Orchestrator {
	if terminalStates == nil {
		terminalStates = []string{"Done", "Cancelled"}
	}
	if activeStates == nil {
		activeStates = []string{"Todo", "In Progress"}
	}
	if byState == nil {
		byState = map[string]int{}
	}
	state := domain.NewOrchestratorState(5000, maxConcurrent)
	return &Orchestrator{
		state: state,
		config: &config.ServiceConfig{
			Tracker: config.TrackerConfig{
				TerminalStates: terminalStates,
				ActiveStates:   activeStates,
			},
			Agent: config.AgentConfig{
				MaxConcurrentAgents:        maxConcurrent,
				MaxRetryBackoffMs:          300000,
				MaxConcurrentAgentsByState: byState,
			},
		},
		tracker: &mockTracker{},
		logger:  slog.Default(),
	}
}

// ---------------------------------------------------------------------------
// 1. sortIssues
// ---------------------------------------------------------------------------

func TestSortIssues_PriorityAscending_NilLast(t *testing.T) {
	issues := []domain.Issue{
		{ID: "no-pri", Identifier: "X-3", Priority: nil},
		{ID: "pri-3", Identifier: "X-2", Priority: intPtr(3)},
		{ID: "pri-1", Identifier: "X-1", Priority: intPtr(1)},
	}
	sortIssues(issues)

	if issues[0].ID != "pri-1" {
		t.Errorf("expected pri-1 first, got %s", issues[0].ID)
	}
	if issues[1].ID != "pri-3" {
		t.Errorf("expected pri-3 second, got %s", issues[1].ID)
	}
	if issues[2].ID != "no-pri" {
		t.Errorf("expected no-pri last, got %s", issues[2].ID)
	}
}

func TestSortIssues_SamePriority_OldestFirst(t *testing.T) {
	t1 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	issues := []domain.Issue{
		{ID: "newer", Identifier: "X-1", Priority: intPtr(2), CreatedAt: timePtr(t2)},
		{ID: "older", Identifier: "X-2", Priority: intPtr(2), CreatedAt: timePtr(t1)},
	}
	sortIssues(issues)

	if issues[0].ID != "older" {
		t.Errorf("expected older first, got %s", issues[0].ID)
	}
	if issues[1].ID != "newer" {
		t.Errorf("expected newer second, got %s", issues[1].ID)
	}
}

func TestSortIssues_SamePriority_SameTime_IdentifierLexicographic(t *testing.T) {
	ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	issues := []domain.Issue{
		{ID: "b", Identifier: "PROJ-200", Priority: intPtr(1), CreatedAt: timePtr(ts)},
		{ID: "a", Identifier: "PROJ-100", Priority: intPtr(1), CreatedAt: timePtr(ts)},
	}
	sortIssues(issues)

	if issues[0].Identifier != "PROJ-100" {
		t.Errorf("expected PROJ-100 first, got %s", issues[0].Identifier)
	}
	if issues[1].Identifier != "PROJ-200" {
		t.Errorf("expected PROJ-200 second, got %s", issues[1].Identifier)
	}
}

func TestSortIssues_NilCreatedAt_SortsAsZeroTime(t *testing.T) {
	ts := time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)
	issues := []domain.Issue{
		{ID: "has-time", Identifier: "X-2", Priority: intPtr(1), CreatedAt: timePtr(ts)},
		{ID: "nil-time", Identifier: "X-1", Priority: intPtr(1), CreatedAt: nil},
	}
	sortIssues(issues)

	// nil CreatedAt maps to zero time, which is before any real timestamp.
	if issues[0].ID != "nil-time" {
		t.Errorf("expected nil-time first (zero time < real time), got %s", issues[0].ID)
	}
}

func TestSortIssues_FullTiebreaker(t *testing.T) {
	t1 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	issues := []domain.Issue{
		{ID: "d", Identifier: "Z-1", Priority: nil, CreatedAt: timePtr(t1)},
		{ID: "c", Identifier: "A-1", Priority: intPtr(2), CreatedAt: timePtr(t2)},
		{ID: "b", Identifier: "A-1", Priority: intPtr(2), CreatedAt: timePtr(t1)},
		{ID: "a", Identifier: "B-1", Priority: intPtr(1), CreatedAt: timePtr(t2)},
	}
	sortIssues(issues)

	expected := []string{"a", "b", "c", "d"}
	for i, e := range expected {
		if issues[i].ID != e {
			t.Errorf("position %d: expected %s, got %s", i, e, issues[i].ID)
		}
	}
}

func TestSortIssues_Empty(t *testing.T) {
	var issues []domain.Issue
	sortIssues(issues) // should not panic
}

func TestSortIssues_SingleElement(t *testing.T) {
	issues := []domain.Issue{{ID: "only", Identifier: "X-1", Priority: intPtr(5)}}
	sortIssues(issues)
	if issues[0].ID != "only" {
		t.Errorf("expected only, got %s", issues[0].ID)
	}
}

// ---------------------------------------------------------------------------
// 2-4. filterEligible
// ---------------------------------------------------------------------------

func TestFilterEligible_TodoWithNonTerminalBlocker_NotEligible(t *testing.T) {
	o := newTestOrchestrator(10, nil, nil, nil)
	inProgress := "In Progress"
	candidates := []domain.Issue{
		{
			ID:         "blocked-1",
			Identifier: "X-1",
			State:      "Todo",
			BlockedBy: []domain.BlockerRef{
				{ID: strPtr("dep-1"), State: &inProgress},
			},
		},
	}

	eligible := o.filterEligible(candidates)
	if len(eligible) != 0 {
		t.Errorf("expected 0 eligible, got %d", len(eligible))
	}
}

func TestFilterEligible_TodoWithTerminalBlockers_IsEligible(t *testing.T) {
	o := newTestOrchestrator(10, nil, nil, nil)
	done := "Done"
	cancelled := "Cancelled"
	candidates := []domain.Issue{
		{
			ID:         "unblocked-1",
			Identifier: "X-1",
			State:      "Todo",
			BlockedBy: []domain.BlockerRef{
				{ID: strPtr("dep-1"), State: &done},
				{ID: strPtr("dep-2"), State: &cancelled},
			},
		},
	}

	eligible := o.filterEligible(candidates)
	if len(eligible) != 1 {
		t.Fatalf("expected 1 eligible, got %d", len(eligible))
	}
	if eligible[0].ID != "unblocked-1" {
		t.Errorf("expected unblocked-1, got %s", eligible[0].ID)
	}
}

func TestFilterEligible_TodoWithNilBlockerState_NotEligible(t *testing.T) {
	o := newTestOrchestrator(10, nil, nil, nil)
	candidates := []domain.Issue{
		{
			ID:         "nil-state-blocker",
			Identifier: "X-1",
			State:      "Todo",
			BlockedBy: []domain.BlockerRef{
				{ID: strPtr("dep-1"), State: nil},
			},
		},
	}

	eligible := o.filterEligible(candidates)
	if len(eligible) != 0 {
		t.Errorf("expected 0 eligible (nil blocker state = unresolved), got %d", len(eligible))
	}
}

func TestFilterEligible_RunningIssueSkipped(t *testing.T) {
	o := newTestOrchestrator(10, nil, nil, nil)
	o.state.Running["running-1"] = &domain.RunningEntry{
		Issue:      &domain.Issue{ID: "running-1"},
		Identifier: "X-1",
	}

	candidates := []domain.Issue{
		{ID: "running-1", Identifier: "X-1", State: "In Progress"},
		{ID: "new-1", Identifier: "X-2", State: "In Progress"},
	}

	eligible := o.filterEligible(candidates)
	if len(eligible) != 1 {
		t.Fatalf("expected 1 eligible, got %d", len(eligible))
	}
	if eligible[0].ID != "new-1" {
		t.Errorf("expected new-1, got %s", eligible[0].ID)
	}
}

func TestFilterEligible_ClaimedIssueSkipped(t *testing.T) {
	o := newTestOrchestrator(10, nil, nil, nil)
	o.state.Claimed["claimed-1"] = true

	candidates := []domain.Issue{
		{ID: "claimed-1", Identifier: "X-1", State: "In Progress"},
		{ID: "new-1", Identifier: "X-2", State: "In Progress"},
	}

	eligible := o.filterEligible(candidates)
	if len(eligible) != 1 {
		t.Fatalf("expected 1 eligible, got %d", len(eligible))
	}
	if eligible[0].ID != "new-1" {
		t.Errorf("expected new-1, got %s", eligible[0].ID)
	}
}

func TestFilterEligible_CompletedIssueSkipped(t *testing.T) {
	o := newTestOrchestrator(10, nil, nil, nil)
	o.state.Completed["done-1"] = true

	candidates := []domain.Issue{
		{ID: "done-1", Identifier: "X-1", State: "Todo"},
	}

	eligible := o.filterEligible(candidates)
	if len(eligible) != 0 {
		t.Errorf("expected 0 eligible, got %d", len(eligible))
	}
}

func TestFilterEligible_RetryingIssueSkipped(t *testing.T) {
	o := newTestOrchestrator(10, nil, nil, nil)
	o.state.RetryAttempts["retry-1"] = &domain.RetryEntry{
		IssueID:    "retry-1",
		Identifier: "X-1",
		Attempt:    2,
	}

	candidates := []domain.Issue{
		{ID: "retry-1", Identifier: "X-1", State: "In Progress"},
	}

	eligible := o.filterEligible(candidates)
	if len(eligible) != 0 {
		t.Errorf("expected 0 eligible (retrying), got %d", len(eligible))
	}
}

func TestFilterEligible_InProgressWithBlockers_StillEligible(t *testing.T) {
	// Blocker rule only applies to "Todo" state, not "In Progress".
	o := newTestOrchestrator(10, nil, nil, nil)
	inProgress := "In Progress"
	candidates := []domain.Issue{
		{
			ID:         "ip-1",
			Identifier: "X-1",
			State:      "In Progress",
			BlockedBy: []domain.BlockerRef{
				{ID: strPtr("dep-1"), State: &inProgress},
			},
		},
	}

	eligible := o.filterEligible(candidates)
	if len(eligible) != 1 {
		t.Errorf("expected 1 eligible (In Progress ignores blockers), got %d", len(eligible))
	}
}

func TestFilterEligible_TodoNoBlockers_IsEligible(t *testing.T) {
	o := newTestOrchestrator(10, nil, nil, nil)
	candidates := []domain.Issue{
		{ID: "todo-1", Identifier: "X-1", State: "Todo", BlockedBy: nil},
	}

	eligible := o.filterEligible(candidates)
	if len(eligible) != 1 {
		t.Errorf("expected 1 eligible, got %d", len(eligible))
	}
}

func TestFilterEligible_EmptyCandidates(t *testing.T) {
	o := newTestOrchestrator(10, nil, nil, nil)
	eligible := o.filterEligible(nil)
	if len(eligible) != 0 {
		t.Errorf("expected 0 eligible, got %d", len(eligible))
	}
}

// ---------------------------------------------------------------------------
// 5. hasUnresolvedBlockers
// ---------------------------------------------------------------------------

func TestHasUnresolvedBlockers_NoBlockers(t *testing.T) {
	issue := domain.Issue{BlockedBy: nil}
	if hasUnresolvedBlockers(issue, []string{"Done"}) {
		t.Error("expected false for issue with no blockers")
	}
}

func TestHasUnresolvedBlockers_AllTerminal(t *testing.T) {
	done := "Done"
	cancelled := "Cancelled"
	issue := domain.Issue{
		BlockedBy: []domain.BlockerRef{
			{State: &done},
			{State: &cancelled},
		},
	}
	if hasUnresolvedBlockers(issue, []string{"Done", "Cancelled"}) {
		t.Error("expected false when all blockers are terminal")
	}
}

func TestHasUnresolvedBlockers_OneNonTerminal(t *testing.T) {
	done := "Done"
	inProgress := "In Progress"
	issue := domain.Issue{
		BlockedBy: []domain.BlockerRef{
			{State: &done},
			{State: &inProgress},
		},
	}
	if !hasUnresolvedBlockers(issue, []string{"Done", "Cancelled"}) {
		t.Error("expected true when one blocker is non-terminal")
	}
}

func TestHasUnresolvedBlockers_NilState(t *testing.T) {
	issue := domain.Issue{
		BlockedBy: []domain.BlockerRef{
			{State: nil},
		},
	}
	if !hasUnresolvedBlockers(issue, []string{"Done"}) {
		t.Error("expected true when blocker state is nil (unknown)")
	}
}

func TestHasUnresolvedBlockers_CaseInsensitive(t *testing.T) {
	done := "DONE"
	issue := domain.Issue{
		BlockedBy: []domain.BlockerRef{
			{State: &done},
		},
	}
	if hasUnresolvedBlockers(issue, []string{"done"}) {
		t.Error("expected false: terminal state match should be case-insensitive")
	}
}

func TestHasUnresolvedBlockers_WhitespaceNormalization(t *testing.T) {
	padded := "  Done  "
	issue := domain.Issue{
		BlockedBy: []domain.BlockerRef{
			{State: &padded},
		},
	}
	if hasUnresolvedBlockers(issue, []string{"Done"}) {
		t.Error("expected false: whitespace should be trimmed")
	}
}

// ---------------------------------------------------------------------------
// 6. normalizeState
// ---------------------------------------------------------------------------

func TestNormalizeState_TrimAndLowercase(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Todo", "todo"},
		{"  In Progress  ", "in progress"},
		{"DONE", "done"},
		{"", ""},
		{" \t Cancelled \n ", "cancelled"},
	}
	for _, tc := range tests {
		got := normalizeState(tc.input)
		if got != tc.expected {
			t.Errorf("normalizeState(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

// ---------------------------------------------------------------------------
// 7. Backoff formula: min(10000 * 2^(attempt-1), max_retry_backoff_ms)
// ---------------------------------------------------------------------------

func TestBackoffDelay_Formula(t *testing.T) {
	o := newTestOrchestrator(10, nil, nil, nil)
	o.config.Agent.MaxRetryBackoffMs = 300000

	tests := []struct {
		attempt  int
		expected int64
	}{
		{1, 10000},                       // 10000 * 2^0 = 10000
		{2, 20000},                       // 10000 * 2^1 = 20000
		{3, 40000},                       // 10000 * 2^2 = 40000
		{4, 80000},                       // 10000 * 2^3 = 80000
		{5, 160000},                      // 10000 * 2^4 = 160000
		{6, 300000},                      // 10000 * 2^5 = 320000 -> capped at 300000
		{10, 300000},                     // capped
	}
	for _, tc := range tests {
		got := o.backoffDelay(tc.attempt)
		if got != tc.expected {
			t.Errorf("backoffDelay(%d) = %d, want %d", tc.attempt, got, tc.expected)
		}
	}
}

func TestBackoffDelay_SmallMax(t *testing.T) {
	o := newTestOrchestrator(10, nil, nil, nil)
	o.config.Agent.MaxRetryBackoffMs = 5000

	// Even attempt 1 (10000) should be capped at 5000.
	got := o.backoffDelay(1)
	if got != 5000 {
		t.Errorf("backoffDelay(1) with max=5000: got %d, want 5000", got)
	}
}

func TestBackoffDelay_ExponentialGrowth(t *testing.T) {
	o := newTestOrchestrator(10, nil, nil, nil)
	o.config.Agent.MaxRetryBackoffMs = math.MaxInt32

	for attempt := 1; attempt <= 5; attempt++ {
		expected := int64(10000 * math.Pow(2, float64(attempt-1)))
		got := o.backoffDelay(attempt)
		if got != expected {
			t.Errorf("backoffDelay(%d) = %d, want %d", attempt, got, expected)
		}
	}
}

// ---------------------------------------------------------------------------
// 8. canDispatch: global concurrency limit
// ---------------------------------------------------------------------------

func TestCanDispatch_GlobalLimit_Allows(t *testing.T) {
	o := newTestOrchestrator(2, nil, nil, nil)
	issue := domain.Issue{ID: "i1", State: "In Progress"}
	if !o.canDispatch(issue) {
		t.Error("expected canDispatch=true when running < max")
	}
}

func TestCanDispatch_GlobalLimit_AtCapacity(t *testing.T) {
	o := newTestOrchestrator(1, nil, nil, nil)
	o.state.Running["existing"] = &domain.RunningEntry{
		Issue: &domain.Issue{ID: "existing", State: "In Progress"},
	}

	issue := domain.Issue{ID: "new", State: "In Progress"}
	if o.canDispatch(issue) {
		t.Error("expected canDispatch=false when at global capacity")
	}
}

func TestCanDispatch_GlobalLimit_ExactBoundary(t *testing.T) {
	o := newTestOrchestrator(3, nil, nil, nil)
	for i := 0; i < 3; i++ {
		id := "r" + string(rune('0'+i))
		o.state.Running[id] = &domain.RunningEntry{
			Issue: &domain.Issue{ID: id, State: "Todo"},
		}
	}

	issue := domain.Issue{ID: "new", State: "Todo"}
	if o.canDispatch(issue) {
		t.Error("expected canDispatch=false when running == max_concurrent")
	}
}

// ---------------------------------------------------------------------------
// 9. canDispatch: per-state concurrency limit
// ---------------------------------------------------------------------------

func TestCanDispatch_PerStateLimit_Allows(t *testing.T) {
	byState := map[string]int{"todo": 2}
	o := newTestOrchestrator(10, nil, nil, byState)
	o.state.Running["r1"] = &domain.RunningEntry{
		Issue: &domain.Issue{ID: "r1", State: "Todo"},
	}

	issue := domain.Issue{ID: "new", State: "Todo"}
	if !o.canDispatch(issue) {
		t.Error("expected canDispatch=true: 1 running todo, limit 2")
	}
}

func TestCanDispatch_PerStateLimit_AtCapacity(t *testing.T) {
	byState := map[string]int{"todo": 1}
	o := newTestOrchestrator(10, nil, nil, byState)
	o.state.Running["r1"] = &domain.RunningEntry{
		Issue: &domain.Issue{ID: "r1", State: "Todo"},
	}

	issue := domain.Issue{ID: "new", State: "Todo"}
	if o.canDispatch(issue) {
		t.Error("expected canDispatch=false: 1 running todo, limit 1")
	}
}

func TestCanDispatch_PerStateLimit_DifferentState_Allows(t *testing.T) {
	byState := map[string]int{"todo": 1}
	o := newTestOrchestrator(10, nil, nil, byState)
	o.state.Running["r1"] = &domain.RunningEntry{
		Issue: &domain.Issue{ID: "r1", State: "Todo"},
	}

	// "In Progress" has no per-state limit, should be allowed.
	issue := domain.Issue{ID: "new", State: "In Progress"}
	if !o.canDispatch(issue) {
		t.Error("expected canDispatch=true: per-state limit is for 'todo', not 'in progress'")
	}
}

func TestCanDispatch_PerStateLimit_CaseInsensitive(t *testing.T) {
	byState := map[string]int{"in progress": 1}
	o := newTestOrchestrator(10, nil, nil, byState)
	o.state.Running["r1"] = &domain.RunningEntry{
		Issue: &domain.Issue{ID: "r1", State: "In Progress"},
	}

	issue := domain.Issue{ID: "new", State: "IN PROGRESS"}
	if o.canDispatch(issue) {
		t.Error("expected canDispatch=false: state comparison should be case-insensitive")
	}
}

func TestCanDispatch_NoPerStateLimit_OnlyGlobal(t *testing.T) {
	o := newTestOrchestrator(10, nil, nil, nil)
	o.state.Running["r1"] = &domain.RunningEntry{
		Issue: &domain.Issue{ID: "r1", State: "Todo"},
	}

	issue := domain.Issue{ID: "new", State: "Todo"}
	if !o.canDispatch(issue) {
		t.Error("expected canDispatch=true: no per-state limit configured, global not reached")
	}
}

// ---------------------------------------------------------------------------
// GetState / StateSnapshot
// ---------------------------------------------------------------------------

func TestGetState_EmptyState(t *testing.T) {
	o := newTestOrchestrator(5, nil, nil, nil)
	snap := o.GetState()

	if len(snap.Running) != 0 {
		t.Errorf("expected 0 running, got %d", len(snap.Running))
	}
	if len(snap.Retrying) != 0 {
		t.Errorf("expected 0 retrying, got %d", len(snap.Retrying))
	}
	if snap.GeneratedAt.IsZero() {
		t.Error("expected GeneratedAt to be set")
	}
}

func TestGetState_WithRunningEntries(t *testing.T) {
	o := newTestOrchestrator(5, nil, nil, nil)
	now := time.Now()
	eventTime := now.Add(-10 * time.Second)
	o.state.Running["issue-1"] = &domain.RunningEntry{
		Issue:      &domain.Issue{ID: "issue-1", State: "In Progress"},
		Identifier: "PROJ-42",
		Session: domain.LiveSession{
			SessionID:        "sess-abc",
			TurnCount:        3,
			LastCodexEvent:   "turn_completed",
			LastCodexMessage: "Working on it",
			LastCodexTimestamp: &eventTime,
			CodexInputTokens:  1000,
			CodexOutputTokens: 500,
			CodexTotalTokens:  1500,
		},
		StartedAt: now,
	}

	snap := o.GetState()
	if len(snap.Running) != 1 {
		t.Fatalf("expected 1 running, got %d", len(snap.Running))
	}

	rs := snap.Running[0]
	if rs.IssueID != "issue-1" {
		t.Errorf("IssueID: got %s, want issue-1", rs.IssueID)
	}
	if rs.IssueIdentifier != "PROJ-42" {
		t.Errorf("Identifier: got %s, want PROJ-42", rs.IssueIdentifier)
	}
	if rs.SessionID != "sess-abc" {
		t.Errorf("SessionID: got %s, want sess-abc", rs.SessionID)
	}
	if rs.TurnCount != 3 {
		t.Errorf("TurnCount: got %d, want 3", rs.TurnCount)
	}
	if rs.LastEvent != "turn_completed" {
		t.Errorf("LastEvent: got %s, want turn_completed", rs.LastEvent)
	}
	if rs.Tokens.InputTokens != 1000 {
		t.Errorf("InputTokens: got %d, want 1000", rs.Tokens.InputTokens)
	}
	if rs.Tokens.OutputTokens != 500 {
		t.Errorf("OutputTokens: got %d, want 500", rs.Tokens.OutputTokens)
	}
	if rs.Tokens.TotalTokens != 1500 {
		t.Errorf("TotalTokens: got %d, want 1500", rs.Tokens.TotalTokens)
	}
}

func TestGetState_WithRetryEntries(t *testing.T) {
	o := newTestOrchestrator(5, nil, nil, nil)
	dueMs := time.Now().Add(30 * time.Second).UnixMilli()
	o.state.RetryAttempts["issue-2"] = &domain.RetryEntry{
		IssueID:    "issue-2",
		Identifier: "PROJ-99",
		Attempt:    3,
		DueAtMs:    dueMs,
		Error:      "agent crashed",
	}

	snap := o.GetState()
	if len(snap.Retrying) != 1 {
		t.Fatalf("expected 1 retrying, got %d", len(snap.Retrying))
	}

	rs := snap.Retrying[0]
	if rs.IssueID != "issue-2" {
		t.Errorf("IssueID: got %s, want issue-2", rs.IssueID)
	}
	if rs.IssueIdentifier != "PROJ-99" {
		t.Errorf("Identifier: got %s, want PROJ-99", rs.IssueIdentifier)
	}
	if rs.Attempt != 3 {
		t.Errorf("Attempt: got %d, want 3", rs.Attempt)
	}
	if rs.Error != "agent crashed" {
		t.Errorf("Error: got %s, want 'agent crashed'", rs.Error)
	}
	// DueAt should be close to dueMs.
	expectedDue := time.UnixMilli(dueMs)
	if !rs.DueAt.Equal(expectedDue) {
		t.Errorf("DueAt: got %v, want %v", rs.DueAt, expectedDue)
	}
}

func TestGetState_CodexTotals(t *testing.T) {
	o := newTestOrchestrator(5, nil, nil, nil)
	o.state.CodexTotals = domain.CodexTotals{
		InputTokens:    5000,
		OutputTokens:   2500,
		TotalTokens:    7500,
		SecondsRunning: 123.45,
	}

	snap := o.GetState()
	if snap.CodexTotals.InputTokens != 5000 {
		t.Errorf("CodexTotals.InputTokens: got %d, want 5000", snap.CodexTotals.InputTokens)
	}
	if snap.CodexTotals.TotalTokens != 7500 {
		t.Errorf("CodexTotals.TotalTokens: got %d, want 7500", snap.CodexTotals.TotalTokens)
	}
	if snap.CodexTotals.SecondsRunning != 123.45 {
		t.Errorf("CodexTotals.SecondsRunning: got %f, want 123.45", snap.CodexTotals.SecondsRunning)
	}
}

// ---------------------------------------------------------------------------
// isTerminalState / isActiveState
// ---------------------------------------------------------------------------

func TestIsTerminalState(t *testing.T) {
	o := newTestOrchestrator(5, []string{"Done", "Cancelled"}, nil, nil)

	tests := []struct {
		state    string
		expected bool
	}{
		{"Done", true},
		{"done", true},
		{" DONE ", true},
		{"Cancelled", true},
		{"In Progress", false},
		{"Todo", false},
		{"", false},
	}
	for _, tc := range tests {
		got := o.isTerminalState(tc.state)
		if got != tc.expected {
			t.Errorf("isTerminalState(%q) = %v, want %v", tc.state, got, tc.expected)
		}
	}
}

func TestIsActiveState(t *testing.T) {
	o := newTestOrchestrator(5, nil, []string{"Todo", "In Progress"}, nil)

	tests := []struct {
		state    string
		expected bool
	}{
		{"Todo", true},
		{"todo", true},
		{"In Progress", true},
		{"in progress", true},
		{"Done", false},
		{"Cancelled", false},
	}
	for _, tc := range tests {
		got := o.isActiveState(tc.state)
		if got != tc.expected {
			t.Errorf("isActiveState(%q) = %v, want %v", tc.state, got, tc.expected)
		}
	}
}

// ---------------------------------------------------------------------------
// handleCodexUpdate
// ---------------------------------------------------------------------------

func TestHandleCodexUpdate_UpdatesSessionFields(t *testing.T) {
	o := newTestOrchestrator(5, nil, nil, nil)
	now := time.Now()
	o.state.Running["issue-1"] = &domain.RunningEntry{
		Issue:      &domain.Issue{ID: "issue-1"},
		Identifier: "X-1",
		Session:    domain.LiveSession{},
	}

	o.handleCodexUpdate(domain.CodexUpdate{
		IssueID:   "issue-1",
		Event:     "turn_completed",
		Timestamp: now,
		PID:       "12345",
		Message:   "Completed turn",
		Usage: &domain.TokenUsage{
			InputTokens:  100,
			OutputTokens: 50,
			TotalTokens:  150,
		},
	})

	entry := o.state.Running["issue-1"]
	if entry.Session.LastCodexEvent != "turn_completed" {
		t.Errorf("LastCodexEvent: got %s, want turn_completed", entry.Session.LastCodexEvent)
	}
	if entry.Session.CodexAppServerPID != "12345" {
		t.Errorf("PID: got %s, want 12345", entry.Session.CodexAppServerPID)
	}
	if entry.Session.TurnCount != 1 {
		t.Errorf("TurnCount: got %d, want 1 (turn_completed increments)", entry.Session.TurnCount)
	}
	if entry.Session.CodexInputTokens != 100 {
		t.Errorf("InputTokens: got %d, want 100", entry.Session.CodexInputTokens)
	}
	if o.state.CodexTotals.InputTokens != 100 {
		t.Errorf("CodexTotals.InputTokens: got %d, want 100", o.state.CodexTotals.InputTokens)
	}
}

func TestHandleCodexUpdate_AccumulatesTokens(t *testing.T) {
	o := newTestOrchestrator(5, nil, nil, nil)
	o.state.Running["issue-1"] = &domain.RunningEntry{
		Issue:   &domain.Issue{ID: "issue-1"},
		Session: domain.LiveSession{},
	}

	for i := 0; i < 3; i++ {
		o.handleCodexUpdate(domain.CodexUpdate{
			IssueID:   "issue-1",
			Event:     "other_message",
			Timestamp: time.Now(),
			Usage: &domain.TokenUsage{
				InputTokens:  10,
				OutputTokens: 5,
				TotalTokens:  15,
			},
		})
	}

	entry := o.state.Running["issue-1"]
	if entry.Session.CodexInputTokens != 30 {
		t.Errorf("accumulated InputTokens: got %d, want 30", entry.Session.CodexInputTokens)
	}
	if o.state.CodexTotals.TotalTokens != 45 {
		t.Errorf("CodexTotals.TotalTokens: got %d, want 45", o.state.CodexTotals.TotalTokens)
	}
}

func TestHandleCodexUpdate_NonTurnCompleted_DoesNotIncrementTurnCount(t *testing.T) {
	o := newTestOrchestrator(5, nil, nil, nil)
	o.state.Running["issue-1"] = &domain.RunningEntry{
		Issue:   &domain.Issue{ID: "issue-1"},
		Session: domain.LiveSession{},
	}

	o.handleCodexUpdate(domain.CodexUpdate{
		IssueID:   "issue-1",
		Event:     "notification",
		Timestamp: time.Now(),
	})

	if o.state.Running["issue-1"].Session.TurnCount != 0 {
		t.Errorf("TurnCount should not increment for non-turn_completed events")
	}
}

func TestHandleCodexUpdate_UnknownIssue_Ignored(t *testing.T) {
	o := newTestOrchestrator(5, nil, nil, nil)
	// Should not panic when issue not in running map.
	o.handleCodexUpdate(domain.CodexUpdate{
		IssueID:   "nonexistent",
		Event:     "turn_completed",
		Timestamp: time.Now(),
	})
}

func TestHandleCodexUpdate_NilUsage_NoAccumulation(t *testing.T) {
	o := newTestOrchestrator(5, nil, nil, nil)
	o.state.Running["issue-1"] = &domain.RunningEntry{
		Issue:   &domain.Issue{ID: "issue-1"},
		Session: domain.LiveSession{},
	}

	o.handleCodexUpdate(domain.CodexUpdate{
		IssueID:   "issue-1",
		Event:     "notification",
		Timestamp: time.Now(),
		Usage:     nil,
	})

	if o.state.Running["issue-1"].Session.CodexInputTokens != 0 {
		t.Error("tokens should remain 0 when usage is nil")
	}
}

func TestHandleCodexUpdate_RateLimits(t *testing.T) {
	o := newTestOrchestrator(5, nil, nil, nil)
	o.state.Running["issue-1"] = &domain.RunningEntry{
		Issue:   &domain.Issue{ID: "issue-1"},
		Session: domain.LiveSession{},
	}

	rl := map[string]int{"requests_remaining": 42}
	o.handleCodexUpdate(domain.CodexUpdate{
		IssueID:    "issue-1",
		Event:      "notification",
		Timestamp:  time.Now(),
		RateLimits: rl,
	})

	if o.state.CodexRateLimits == nil {
		t.Error("expected rate limits to be set")
	}
}

// ---------------------------------------------------------------------------
// priorityVal / createdAtVal helpers
// ---------------------------------------------------------------------------

func TestPriorityVal_Nil(t *testing.T) {
	if priorityVal(nil) != math.MaxInt {
		t.Errorf("nil priority should return MaxInt, got %d", priorityVal(nil))
	}
}

func TestPriorityVal_Value(t *testing.T) {
	p := intPtr(3)
	if priorityVal(p) != 3 {
		t.Errorf("expected 3, got %d", priorityVal(p))
	}
}

func TestCreatedAtVal_Nil(t *testing.T) {
	zero := time.Time{}
	if !createdAtVal(nil).Equal(zero) {
		t.Error("nil createdAt should return zero time")
	}
}

func TestCreatedAtVal_Value(t *testing.T) {
	ts := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	if !createdAtVal(&ts).Equal(ts) {
		t.Error("createdAtVal should return the pointed-to time")
	}
}

// ---------------------------------------------------------------------------
// RequestRefresh (non-blocking)
// ---------------------------------------------------------------------------

func TestRequestRefresh_NonBlocking(t *testing.T) {
	o := newTestOrchestrator(5, nil, nil, nil)
	o.refreshCh = make(chan struct{}, 1)

	// First call should succeed.
	o.RequestRefresh()
	// Second call should not block (buffer full, drops silently).
	o.RequestRefresh()

	select {
	case <-o.refreshCh:
		// OK
	default:
		t.Error("expected refresh signal in channel")
	}
}
