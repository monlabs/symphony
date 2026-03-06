package tracker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/monlabs/symphony/internal/domain"
)

func TestNewClient_Linear(t *testing.T) {
	c, err := NewClient("linear", "https://example.com/graphql", "test-key", "my-project", []string{"Todo"}, []string{"Done"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestNewClient_UnsupportedKind(t *testing.T) {
	_, err := NewClient("jira", "", "", "", nil, nil)
	if err == nil {
		t.Fatal("expected error for unsupported tracker kind")
	}
}

func TestNewClient_MissingAPIKey(t *testing.T) {
	_, err := NewClient("linear", "", "", "slug", nil, nil)
	if err == nil {
		t.Fatal("expected error for missing API key")
	}
}

func TestNewClient_MissingProjectSlug(t *testing.T) {
	_, err := NewClient("linear", "", "key", "", nil, nil)
	if err == nil {
		t.Fatal("expected error for missing project slug")
	}
}

func TestFetchIssuesByStates_Empty(t *testing.T) {
	c := NewLinearClient("https://example.com", "key", "slug", []string{"Todo"})
	issues, err := c.FetchIssuesByStates(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("expected empty result, got %d", len(issues))
	}
}

func TestFetchIssueStatesByIDs_Empty(t *testing.T) {
	c := NewLinearClient("https://example.com", "key", "slug", []string{"Todo"})
	issues, err := c.FetchIssueStatesByIDs(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("expected empty result, got %d", len(issues))
	}
}

func TestFetchCandidateIssues_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "test-token" {
			t.Error("expected Authorization header")
		}

		resp := map[string]interface{}{
			"data": map[string]interface{}{
				"issues": map[string]interface{}{
					"pageInfo": map[string]interface{}{
						"hasNextPage": false,
						"endCursor":   nil,
					},
					"nodes": []map[string]interface{}{
						{
							"id":         "issue-1",
							"identifier": "PROJ-1",
							"title":      "Test Issue",
							"priority":   1,
							"state":      map[string]string{"name": "Todo"},
							"labels":     map[string]interface{}{"nodes": []map[string]string{{"name": "Bug"}, {"name": "P1"}}},
							"inverseRelations": map[string]interface{}{
								"nodes": []map[string]interface{}{
									{
										"type": "blocks",
										"issue": map[string]interface{}{
											"id":         "blocker-1",
											"identifier": "PROJ-2",
											"state":      map[string]string{"name": "Done"},
										},
									},
								},
							},
							"createdAt": "2025-01-01T00:00:00Z",
							"updatedAt": "2025-01-02T00:00:00Z",
						},
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewLinearClient(srv.URL, "test-token", "my-project", []string{"Todo"})
	issues, err := c.FetchCandidateIssues()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}

	issue := issues[0]
	if issue.ID != "issue-1" {
		t.Errorf("expected id=issue-1, got %s", issue.ID)
	}
	if issue.Identifier != "PROJ-1" {
		t.Errorf("expected identifier=PROJ-1, got %s", issue.Identifier)
	}
	if issue.State != "Todo" {
		t.Errorf("expected state=Todo, got %s", issue.State)
	}
	// Labels should be lowercase
	if len(issue.Labels) != 2 || issue.Labels[0] != "bug" || issue.Labels[1] != "p1" {
		t.Errorf("expected lowercase labels [bug, p1], got %v", issue.Labels)
	}
	// BlockedBy
	if len(issue.BlockedBy) != 1 {
		t.Fatalf("expected 1 blocker, got %d", len(issue.BlockedBy))
	}
	if *issue.BlockedBy[0].ID != "blocker-1" {
		t.Errorf("expected blocker id=blocker-1, got %s", *issue.BlockedBy[0].ID)
	}
	if *issue.BlockedBy[0].State != "Done" {
		t.Errorf("expected blocker state=Done, got %s", *issue.BlockedBy[0].State)
	}
}

func TestFetchIssueStatesByIDs_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"data": map[string]interface{}{
				"issues": map[string]interface{}{
					"nodes": []map[string]interface{}{
						{
							"id":         "id1",
							"identifier": "PROJ-1",
							"title":      "Issue 1",
							"state":      map[string]string{"name": "In Progress"},
						},
					},
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewLinearClient(srv.URL, "key", "slug", []string{"Todo"})
	issues, err := c.FetchIssueStatesByIDs([]string{"id1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}
	if issues[0].State != "In Progress" {
		t.Errorf("expected state=In Progress, got %s", issues[0].State)
	}
}

func TestGraphQLErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"errors": []map[string]string{
				{"message": "something went wrong"},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewLinearClient(srv.URL, "key", "slug", []string{"Todo"})
	_, err := c.FetchCandidateIssues()
	if err == nil {
		t.Fatal("expected error for GraphQL errors")
	}
}

func TestNonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("server error"))
	}))
	defer srv.Close()

	c := NewLinearClient(srv.URL, "key", "slug", []string{"Todo"})
	_, err := c.FetchCandidateIssues()
	if err == nil {
		t.Fatal("expected error for non-200 status")
	}
}

func TestNormalizeCandidateIssue(t *testing.T) {
	desc := "a description"
	branch := "feature/test"
	url := "https://linear.app/issue/1"
	pri := 2

	li := &linearIssue{
		ID:          "id1",
		Identifier:  "PROJ-1",
		Title:       "Test",
		Description: &desc,
		Priority:    &pri,
		State:       &linearState{Name: "Todo"},
		BranchName:  &branch,
		URL:         &url,
		Labels: &linearLabelConn{
			Nodes: []linearLabel{{Name: "BUG"}, {Name: "Enhancement"}},
		},
		InverseRelations: nil,
	}

	issue := normalizeCandidateIssue(li)

	if issue.ID != "id1" {
		t.Errorf("expected id=id1, got %s", issue.ID)
	}
	if *issue.Description != desc {
		t.Errorf("wrong description")
	}
	if *issue.Priority != 2 {
		t.Errorf("expected priority=2, got %d", *issue.Priority)
	}
	if len(issue.Labels) != 2 || issue.Labels[0] != "bug" || issue.Labels[1] != "enhancement" {
		t.Errorf("labels not normalized to lowercase: %v", issue.Labels)
	}
	if len(issue.BlockedBy) != 0 {
		t.Errorf("expected no blockers, got %d", len(issue.BlockedBy))
	}
}

func TestDeriveBlockedBy(t *testing.T) {
	inv := &linearInverseConn{
		Nodes: []linearInverseRelation{
			{
				Type: "blocks",
				Issue: linearRelRef{
					ID:         "b1",
					Identifier: "PROJ-2",
					State:      &linearState{Name: "In Progress"},
				},
			},
			{
				Type: "relates_to", // should be ignored
				Issue: linearRelRef{
					ID: "b2",
				},
			},
		},
	}

	refs := deriveBlockedBy(inv)
	if len(refs) != 1 {
		t.Fatalf("expected 1 blocker, got %d", len(refs))
	}
	if *refs[0].ID != "b1" {
		t.Errorf("expected blocker id=b1, got %s", *refs[0].ID)
	}
}

func TestDeriveBlockedBy_Nil(t *testing.T) {
	refs := deriveBlockedBy(nil)
	if refs != nil {
		t.Errorf("expected nil, got %v", refs)
	}
}

func TestPagination(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)

		var resp map[string]interface{}
		if callCount == 1 {
			cursor := "page2"
			resp = map[string]interface{}{
				"data": map[string]interface{}{
					"issues": map[string]interface{}{
						"pageInfo": map[string]interface{}{"hasNextPage": true, "endCursor": cursor},
						"nodes": []map[string]interface{}{
							{"id": "id1", "identifier": "P-1", "title": "A", "state": map[string]string{"name": "Todo"}},
						},
					},
				},
			}
		} else {
			resp = map[string]interface{}{
				"data": map[string]interface{}{
					"issues": map[string]interface{}{
						"pageInfo": map[string]interface{}{"hasNextPage": false},
						"nodes": []map[string]interface{}{
							{"id": "id2", "identifier": "P-2", "title": "B", "state": map[string]string{"name": "Todo"}},
						},
					},
				},
			}
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewLinearClient(srv.URL, "key", "slug", []string{"Todo"})
	issues, err := c.FetchCandidateIssues()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("expected 2 issues across pages, got %d", len(issues))
	}
	if callCount != 2 {
		t.Errorf("expected 2 API calls for pagination, got %d", callCount)
	}
}

func TestMissingEndCursor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"data": map[string]interface{}{
				"issues": map[string]interface{}{
					"pageInfo": map[string]interface{}{"hasNextPage": true, "endCursor": nil},
					"nodes": []map[string]interface{}{
						{"id": "id1", "identifier": "P-1", "title": "A", "state": map[string]string{"name": "Todo"}},
					},
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewLinearClient(srv.URL, "key", "slug", []string{"Todo"})
	_, err := c.FetchCandidateIssues()
	if err == nil {
		t.Fatal("expected ErrLinearMissingEndCursor")
	}
}

// Verify the Issue interface compliance
func TestLinearClientImplementsClient(t *testing.T) {
	var _ Client = (*LinearClient)(nil)
}

// Verify query uses correct variable type for IDs
func TestIssueStatesByIDsQuery(t *testing.T) {
	if !contains(issueStatesByIDsQuery, "$ids: [ID!]!") {
		t.Error("query should use [ID!]! type for ids variable")
	}
}

func TestCandidateIssueQuery_ProjectFilter(t *testing.T) {
	if !contains(candidateIssuesQuery, "slugId") {
		t.Error("candidate query should use slugId for project filter")
	}
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && stringContains(s, substr)
}

func stringContains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// Suppress unused import
var _ = domain.Issue{}
