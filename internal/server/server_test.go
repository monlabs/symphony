package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type mockProvider struct {
	snapshot     StateSnapshot
	issueDetail *IssueDetail
	issueErr    error
	refreshed   bool
}

func (m *mockProvider) GetStateSnapshot() StateSnapshot {
	return m.snapshot
}

func (m *mockProvider) GetIssueDetail(identifier string) (*IssueDetail, error) {
	if m.issueErr != nil {
		return nil, m.issueErr
	}
	return m.issueDetail, nil
}

func (m *mockProvider) RequestRefresh() {
	m.refreshed = true
}

func newTestProvider() *mockProvider {
	return &mockProvider{
		snapshot: StateSnapshot{
			GeneratedAt: "2025-01-01T00:00:00Z",
			Counts:      SnapshotCounts{Running: 1, Retrying: 0},
			Running: []RunningRow{
				{
					IssueID:         "id1",
					IssueIdentifier: "P-1",
					State:           "In Progress",
					SessionID:       "thread-1-turn-1",
					TurnCount:       3,
					LastEvent:       "notification",
					StartedAt:       "2025-01-01T00:00:00Z",
					Tokens:          TokensJSON{InputTokens: 100, OutputTokens: 50, TotalTokens: 150},
				},
			},
			Retrying:    []RetryRow{},
			CodexTotals: CodexTotalsJSON{InputTokens: 500, OutputTokens: 200, TotalTokens: 700, SecondsRunning: 120.5},
		},
	}
}

func TestGetState(t *testing.T) {
	provider := newTestProvider()
	srv := New(provider, 0)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/state", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var snap StateSnapshot
	if err := json.NewDecoder(w.Body).Decode(&snap); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if snap.Counts.Running != 1 {
		t.Errorf("expected 1 running, got %d", snap.Counts.Running)
	}
	if len(snap.Running) != 1 {
		t.Fatalf("expected 1 running row, got %d", len(snap.Running))
	}
	if snap.Running[0].IssueIdentifier != "P-1" {
		t.Errorf("expected P-1, got %s", snap.Running[0].IssueIdentifier)
	}
}

func TestGetState_MethodNotAllowed(t *testing.T) {
	provider := newTestProvider()
	srv := New(provider, 0)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/state", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestRefresh(t *testing.T) {
	provider := newTestProvider()
	srv := New(provider, 0)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/refresh", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("expected 202, got %d", w.Code)
	}
	if !provider.refreshed {
		t.Error("expected RequestRefresh to be called")
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["queued"] != true {
		t.Error("expected queued=true")
	}
}

func TestRefresh_MethodNotAllowed(t *testing.T) {
	provider := newTestProvider()
	srv := New(provider, 0)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/refresh", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestIssueDetail_Found(t *testing.T) {
	provider := newTestProvider()
	provider.issueDetail = &IssueDetail{
		IssueIdentifier: "P-1",
		IssueID:         "id1",
		Status:          "running",
	}

	srv := New(provider, 0)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/P-1", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var detail IssueDetail
	json.NewDecoder(w.Body).Decode(&detail)
	if detail.IssueIdentifier != "P-1" {
		t.Errorf("expected P-1, got %s", detail.IssueIdentifier)
	}
}

func TestIssueDetail_NotFound(t *testing.T) {
	provider := newTestProvider()
	provider.issueErr = fmt.Errorf("not found")

	srv := New(provider, 0)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/P-999", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestDashboard(t *testing.T) {
	provider := newTestProvider()
	srv := New(provider, 0)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Errorf("expected text/html content type, got %s", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Symphony Dashboard") {
		t.Error("dashboard should contain title")
	}
}

func TestDashboard_MethodNotAllowed(t *testing.T) {
	provider := newTestProvider()
	srv := New(provider, 0)

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestServerStart(t *testing.T) {
	provider := newTestProvider()
	srv := New(provider, 0) // ephemeral port

	addr, err := srv.Start()
	if err != nil {
		t.Fatalf("failed to start: %v", err)
	}
	if addr == "" {
		t.Fatal("expected non-empty address")
	}
	t.Logf("server listening at %s", addr)

	// Verify we can reach the API
	resp, err := http.Get("http://" + addr + "/api/v1/state")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestDashboard_404ForUnknownPaths(t *testing.T) {
	provider := newTestProvider()
	srv := New(provider, 0)

	req := httptest.NewRequest(http.MethodGet, "/nonexistent", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for unknown path, got %d", w.Code)
	}
}
