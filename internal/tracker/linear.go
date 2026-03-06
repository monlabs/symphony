package tracker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/monlabs/symphony/internal/domain"
)

const (
	defaultPageSize      = 50
	defaultNetworkTimeout = 30 * time.Second
)

// LinearClient implements the Client interface for the Linear issue tracker.
type LinearClient struct {
	endpoint     string
	apiKey       string
	projectSlug  string
	activeStates []string
	httpClient   *http.Client
}

// NewLinearClient creates a new LinearClient.
func NewLinearClient(endpoint, apiKey, projectSlug string, activeStates []string) *LinearClient {
	return &LinearClient{
		endpoint:     endpoint,
		apiKey:       apiKey,
		projectSlug:  projectSlug,
		activeStates: activeStates,
		httpClient: &http.Client{
			Timeout: defaultNetworkTimeout,
		},
	}
}

// ---------------------------------------------------------------------------
// GraphQL queries
// ---------------------------------------------------------------------------

const candidateIssuesQuery = `query($projectSlug: String!, $stateNames: [String!]!, $cursor: String) {
  issues(
    filter: {
      project: { slugId: { eq: $projectSlug } }
      state: { name: { in: $stateNames } }
    }
    first: 50
    after: $cursor
    orderBy: createdAt
  ) {
    pageInfo { hasNextPage endCursor }
    nodes {
      id identifier title description priority
      state { name }
      branchName url
      labels { nodes { name } }
      relations { nodes { type relatedIssue { id identifier state { name } } } }
      inverseRelations { nodes { type issue { id identifier state { name } } } }
      createdAt updatedAt
    }
  }
}`

const issuesByStatesQuery = `query($projectSlug: String!, $stateNames: [String!]!, $cursor: String) {
  issues(
    filter: {
      project: { slugId: { eq: $projectSlug } }
      state: { name: { in: $stateNames } }
    }
    first: 50
    after: $cursor
  ) {
    pageInfo { hasNextPage endCursor }
    nodes { id identifier state { name } }
  }
}`

const issueStatesByIDsQuery = `query($ids: [ID!]!) {
  issues(filter: { id: { in: $ids } }) {
    nodes {
      id identifier title state { name }
    }
  }
}`

// ---------------------------------------------------------------------------
// GraphQL request / response types
// ---------------------------------------------------------------------------

type graphQLRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

type graphQLResponse struct {
	Data   json.RawMessage  `json:"data"`
	Errors []graphQLError   `json:"errors,omitempty"`
}

type graphQLError struct {
	Message string `json:"message"`
}

// ---------------------------------------------------------------------------
// Linear-specific response shapes
// ---------------------------------------------------------------------------

type issuesData struct {
	Issues issuesConnection `json:"issues"`
}

type issuesConnection struct {
	PageInfo pageInfo        `json:"pageInfo"`
	Nodes    []linearIssue   `json:"nodes"`
}

type pageInfo struct {
	HasNextPage bool    `json:"hasNextPage"`
	EndCursor   *string `json:"endCursor"`
}

type linearIssue struct {
	ID               string              `json:"id"`
	Identifier       string              `json:"identifier"`
	Title            string              `json:"title"`
	Description      *string             `json:"description"`
	Priority         *int                `json:"priority"`
	State            *linearState        `json:"state"`
	BranchName       *string             `json:"branchName"`
	URL              *string             `json:"url"`
	Labels           *linearLabelConn    `json:"labels"`
	Relations        *linearRelationConn `json:"relations"`
	InverseRelations *linearInverseConn  `json:"inverseRelations"`
	CreatedAt        *string             `json:"createdAt"`
	UpdatedAt        *string             `json:"updatedAt"`
}

type linearState struct {
	Name string `json:"name"`
}

type linearLabelConn struct {
	Nodes []linearLabel `json:"nodes"`
}

type linearLabel struct {
	Name string `json:"name"`
}

type linearRelationConn struct {
	Nodes []linearRelation `json:"nodes"`
}

type linearRelation struct {
	Type         string       `json:"type"`
	RelatedIssue linearRelRef `json:"relatedIssue"`
}

type linearRelRef struct {
	ID         string       `json:"id"`
	Identifier string       `json:"identifier"`
	State      *linearState `json:"state"`
}

type linearInverseConn struct {
	Nodes []linearInverseRelation `json:"nodes"`
}

type linearInverseRelation struct {
	Type  string       `json:"type"`
	Issue linearRelRef `json:"issue"`
}

type nodesData struct {
	Nodes []json.RawMessage `json:"nodes"`
}

type minimalLinearIssue struct {
	ID         string       `json:"id"`
	Identifier string       `json:"identifier"`
	Title      string       `json:"title"`
	State      *linearState `json:"state"`
}

// ---------------------------------------------------------------------------
// Client interface methods
// ---------------------------------------------------------------------------

// FetchCandidateIssues returns issues in the configured active states.
func (c *LinearClient) FetchCandidateIssues() ([]domain.Issue, error) {
	var allIssues []domain.Issue
	var cursor *string

	for {
		vars := map[string]any{
			"projectSlug": c.projectSlug,
			"stateNames":  c.activeStates,
		}
		if cursor != nil {
			vars["cursor"] = *cursor
		}

		respBody, err := c.doGraphQL(candidateIssuesQuery, vars)
		if err != nil {
			return nil, err
		}

		var data issuesData
		if err := json.Unmarshal(respBody, &data); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrLinearUnknownPayload, err)
		}

		for i := range data.Issues.Nodes {
			issue := normalizeCandidateIssue(&data.Issues.Nodes[i])
			allIssues = append(allIssues, issue)
		}

		if !data.Issues.PageInfo.HasNextPage {
			break
		}
		if data.Issues.PageInfo.EndCursor == nil {
			return nil, ErrLinearMissingEndCursor
		}
		cursor = data.Issues.PageInfo.EndCursor
	}

	return allIssues, nil
}

// FetchIssuesByStates returns issues in the given states for terminal cleanup.
// An empty stateNames slice returns an empty result without making an API call.
func (c *LinearClient) FetchIssuesByStates(stateNames []string) ([]domain.Issue, error) {
	if len(stateNames) == 0 {
		return nil, nil
	}

	var allIssues []domain.Issue
	var cursor *string

	for {
		vars := map[string]any{
			"projectSlug": c.projectSlug,
			"stateNames":  stateNames,
		}
		if cursor != nil {
			vars["cursor"] = *cursor
		}

		respBody, err := c.doGraphQL(issuesByStatesQuery, vars)
		if err != nil {
			return nil, err
		}

		var data issuesData
		if err := json.Unmarshal(respBody, &data); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrLinearUnknownPayload, err)
		}

		for i := range data.Issues.Nodes {
			n := &data.Issues.Nodes[i]
			issue := domain.Issue{
				ID:         n.ID,
				Identifier: n.Identifier,
			}
			if n.State != nil {
				issue.State = n.State.Name
			}
			allIssues = append(allIssues, issue)
		}

		if !data.Issues.PageInfo.HasNextPage {
			break
		}
		if data.Issues.PageInfo.EndCursor == nil {
			return nil, ErrLinearMissingEndCursor
		}
		cursor = data.Issues.PageInfo.EndCursor
	}

	return allIssues, nil
}

// FetchIssueStatesByIDs returns minimal issue records with current state for reconciliation.
func (c *LinearClient) FetchIssueStatesByIDs(issueIDs []string) ([]domain.Issue, error) {
	if len(issueIDs) == 0 {
		return nil, nil
	}

	vars := map[string]any{
		"ids": issueIDs,
	}

	respBody, err := c.doGraphQL(issueStatesByIDsQuery, vars)
	if err != nil {
		return nil, err
	}

	var data issuesData
	if err := json.Unmarshal(respBody, &data); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrLinearUnknownPayload, err)
	}

	var issues []domain.Issue
	for i := range data.Issues.Nodes {
		n := &data.Issues.Nodes[i]
		if n.ID == "" {
			continue
		}
		issue := domain.Issue{
			ID:         n.ID,
			Identifier: n.Identifier,
			Title:      n.Title,
		}
		if n.State != nil {
			issue.State = n.State.Name
		}
		issues = append(issues, issue)
	}

	return issues, nil
}

// ---------------------------------------------------------------------------
// HTTP / GraphQL helper
// ---------------------------------------------------------------------------

func (c *LinearClient) doGraphQL(query string, variables map[string]any) (json.RawMessage, error) {
	reqBody := graphQLRequest{
		Query:     query,
		Variables: variables,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal request: %v", ErrLinearAPIRequest, err)
	}

	req, err := http.NewRequest(http.MethodPost, c.endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("%w: build request: %v", ErrLinearAPIRequest, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrLinearAPIRequest, err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: read response: %v", ErrLinearAPIRequest, err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d, body: %s", ErrLinearAPIStatus, resp.StatusCode, truncate(string(respBytes), 512))
	}

	var gqlResp graphQLResponse
	if err := json.Unmarshal(respBytes, &gqlResp); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrLinearUnknownPayload, err)
	}

	if len(gqlResp.Errors) > 0 {
		msgs := make([]string, len(gqlResp.Errors))
		for i, e := range gqlResp.Errors {
			msgs[i] = e.Message
		}
		return nil, fmt.Errorf("%w: %s", ErrLinearGraphQLErrors, strings.Join(msgs, "; "))
	}

	if gqlResp.Data == nil {
		return nil, fmt.Errorf("%w: data field is nil", ErrLinearUnknownPayload)
	}

	return gqlResp.Data, nil
}

// ---------------------------------------------------------------------------
// Normalization helpers (Section 11.3)
// ---------------------------------------------------------------------------

func normalizeCandidateIssue(n *linearIssue) domain.Issue {
	issue := domain.Issue{
		ID:          n.ID,
		Identifier:  n.Identifier,
		Title:       n.Title,
		Description: n.Description,
		Priority:    n.Priority,
		BranchName:  n.BranchName,
		URL:         n.URL,
	}

	// State
	if n.State != nil {
		issue.State = n.State.Name
	}

	// Labels -> lowercase
	if n.Labels != nil {
		for _, l := range n.Labels.Nodes {
			issue.Labels = append(issue.Labels, strings.ToLower(l.Name))
		}
	}
	if issue.Labels == nil {
		issue.Labels = []string{}
	}

	// BlockedBy -> derived from inverseRelations where type is "blocks"
	issue.BlockedBy = deriveBlockedBy(n.InverseRelations)
	if issue.BlockedBy == nil {
		issue.BlockedBy = []domain.BlockerRef{}
	}

	// Timestamps -> parse ISO-8601
	if n.CreatedAt != nil {
		if t, err := time.Parse(time.RFC3339, *n.CreatedAt); err == nil {
			issue.CreatedAt = &t
		}
	}
	if n.UpdatedAt != nil {
		if t, err := time.Parse(time.RFC3339, *n.UpdatedAt); err == nil {
			issue.UpdatedAt = &t
		}
	}

	return issue
}

// deriveBlockedBy extracts blocker references from inverse relations where type is "blocks".
// When another issue has a "blocks" relation pointing to the current issue, that means the
// current issue is blocked by the other issue.
func deriveBlockedBy(inv *linearInverseConn) []domain.BlockerRef {
	if inv == nil {
		return nil
	}
	var refs []domain.BlockerRef
	for _, rel := range inv.Nodes {
		if strings.ToLower(rel.Type) == "blocks" {
			ref := domain.BlockerRef{
				ID:         strPtr(rel.Issue.ID),
				Identifier: strPtr(rel.Issue.Identifier),
			}
			if rel.Issue.State != nil {
				ref.State = strPtr(rel.Issue.State.Name)
			}
			refs = append(refs, ref)
		}
	}
	return refs
}

func strPtr(s string) *string {
	return &s
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
