package tracker

import (
	"errors"
	"fmt"

	"github.com/monlabs/symphony/internal/domain"
)

// Client defines the interface for issue tracker integrations.
type Client interface {
	// FetchCandidateIssues returns issues in configured active states for the configured project.
	FetchCandidateIssues() ([]domain.Issue, error)

	// FetchIssuesByStates returns issues in given states (for startup terminal cleanup).
	FetchIssuesByStates(stateNames []string) ([]domain.Issue, error)

	// FetchIssueStatesByIDs returns minimal issue records with current state (for reconciliation).
	FetchIssueStatesByIDs(issueIDs []string) ([]domain.Issue, error)
}

// Sentinel errors (Section 11.4).
var (
	ErrUnsupportedTrackerKind = errors.New("unsupported tracker kind")
	ErrMissingTrackerAPIKey   = errors.New("missing tracker API key")
	ErrMissingProjectSlug     = errors.New("missing project slug")
	ErrLinearAPIRequest       = errors.New("linear API request failed")
	ErrLinearAPIStatus        = errors.New("linear API returned non-200 status")
	ErrLinearGraphQLErrors    = errors.New("linear GraphQL response contained errors")
	ErrLinearUnknownPayload   = errors.New("linear API returned unknown payload structure")
	ErrLinearMissingEndCursor = errors.New("linear API indicated more pages but endCursor is missing")
)

// NewClient creates a tracker client for the given kind.
// Supported kinds: "linear".
func NewClient(kind, endpoint, apiKey, projectSlug string, activeStates, terminalStates []string) (Client, error) {
	switch kind {
	case "linear":
		if apiKey == "" {
			return nil, ErrMissingTrackerAPIKey
		}
		if projectSlug == "" {
			return nil, ErrMissingProjectSlug
		}
		if endpoint == "" {
			endpoint = "https://api.linear.app/graphql"
		}
		return NewLinearClient(endpoint, apiKey, projectSlug, activeStates), nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedTrackerKind, kind)
	}
}
