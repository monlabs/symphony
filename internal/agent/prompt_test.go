package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/monlabs/symphony/internal/domain"
)

func TestBuildPrompt_EmptyTemplate(t *testing.T) {
	issue := &domain.Issue{ID: "1", Identifier: "P-1", Title: "Test"}
	result, err := BuildPrompt("", issue, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "You are working on an issue from Linear." {
		t.Errorf("expected default prompt, got: %s", result)
	}
}

func TestBuildPrompt_SimpleTemplate(t *testing.T) {
	issue := &domain.Issue{
		ID:         "id1",
		Identifier: "PROJ-42",
		Title:      "Fix the bug",
		State:      "Todo",
		Labels:     []string{"bug"},
		BlockedBy:  []domain.BlockerRef{},
	}
	tmpl := "Work on {{ issue.identifier }}: {{ issue.title }}"
	result, err := BuildPrompt(tmpl, issue, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Work on PROJ-42: Fix the bug" {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestBuildPrompt_WithAttempt(t *testing.T) {
	issue := &domain.Issue{
		ID:         "id1",
		Identifier: "P-1",
		Title:      "Test",
		State:      "Todo",
		Labels:     []string{},
		BlockedBy:  []domain.BlockerRef{},
	}
	attempt := 3
	tmpl := "Attempt {{ attempt }} for {{ issue.identifier }}"
	result, err := BuildPrompt(tmpl, issue, &attempt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Attempt 3 for P-1" {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestBuildPrompt_ForLoop(t *testing.T) {
	issue := &domain.Issue{
		ID:         "id1",
		Identifier: "P-1",
		Title:      "Test",
		State:      "Todo",
		Labels:     []string{"bug", "urgent", "p0"},
		BlockedBy:  []domain.BlockerRef{},
	}
	tmpl := "Labels:{% for label in issue.labels %} {{ label }}{% endfor %}"
	result, err := BuildPrompt(tmpl, issue, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Labels: bug urgent p0" {
		t.Errorf("unexpected result: %q", result)
	}
}

func TestBuildPrompt_IfConditional(t *testing.T) {
	issue := &domain.Issue{
		ID:         "id1",
		Identifier: "P-1",
		Title:      "Test",
		State:      "Todo",
		Labels:     []string{},
		BlockedBy:  []domain.BlockerRef{},
	}

	// With attempt
	attempt := 2
	tmpl := "{% if attempt %}Retry #{{ attempt }}{% else %}First run{% endif %}"
	result, err := BuildPrompt(tmpl, issue, &attempt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Retry #2" {
		t.Errorf("unexpected result: %q", result)
	}

	// Without attempt
	result2, err := BuildPrompt(tmpl, issue, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result2 != "First run" {
		t.Errorf("unexpected result: %q", result2)
	}
}

func TestBuildPrompt_Filters(t *testing.T) {
	issue := &domain.Issue{
		ID:         "id1",
		Identifier: "P-1",
		Title:      "fix the bug",
		State:      "Todo",
		Labels:     []string{},
		BlockedBy:  []domain.BlockerRef{},
	}
	tmpl := "{{ issue.title | upcase }}"
	result, err := BuildPrompt(tmpl, issue, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "FIX THE BUG" {
		t.Errorf("unexpected result: %q", result)
	}
}

func TestBuildPrompt_BlockedByIteration(t *testing.T) {
	bID := "b1"
	bIdent := "P-2"
	bState := "In Progress"
	issue := &domain.Issue{
		ID:         "id1",
		Identifier: "P-1",
		Title:      "Test",
		State:      "Todo",
		Labels:     []string{},
		BlockedBy: []domain.BlockerRef{
			{ID: &bID, Identifier: &bIdent, State: &bState},
		},
	}
	tmpl := "Blockers:{% for b in issue.blocked_by %} {{ b.identifier }}({{ b.state }}){% endfor %}"
	result, err := BuildPrompt(tmpl, issue, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Blockers: P-2(In Progress)" {
		t.Errorf("unexpected result: %q", result)
	}
}

func TestBuildPrompt_NestedFields(t *testing.T) {
	desc := "A description"
	issue := &domain.Issue{
		ID:          "id1",
		Identifier:  "P-1",
		Title:       "Test",
		Description: &desc,
		State:       "In Progress",
		Labels:      []string{},
		BlockedBy:   []domain.BlockerRef{},
	}
	tmpl := "{{ issue.state }} - {{ issue.description }}"
	result, err := BuildPrompt(tmpl, issue, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "In Progress - A description" {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestBuildPrompt_NilDescription(t *testing.T) {
	issue := &domain.Issue{
		ID:         "id1",
		Identifier: "P-1",
		Title:      "Test",
		State:      "Todo",
		Labels:     []string{},
		BlockedBy:  []domain.BlockerRef{},
	}
	tmpl := "desc={{ issue.description }}"
	result, err := BuildPrompt(tmpl, issue, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "desc=" {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestBuildPrompt_AllFields(t *testing.T) {
	now := time.Now()
	desc := "desc"
	branch := "main"
	url := "https://example.com"
	pri := 1
	blockerID := "b1"
	blockerIdent := "P-2"
	blockerState := "Done"

	issue := &domain.Issue{
		ID:          "id1",
		Identifier:  "P-1",
		Title:       "Title",
		Description: &desc,
		Priority:    &pri,
		State:       "Todo",
		BranchName:  &branch,
		URL:         &url,
		Labels:      []string{"bug", "urgent"},
		BlockedBy: []domain.BlockerRef{
			{ID: &blockerID, Identifier: &blockerIdent, State: &blockerState},
		},
		CreatedAt: &now,
		UpdatedAt: &now,
	}

	tmpl := "{{ issue.id }} {{ issue.identifier }} {{ issue.title }} {{ issue.state }}"
	result, err := BuildPrompt(tmpl, issue, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "id1") || !strings.Contains(result, "P-1") {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestBuildPrompt_SyntaxError(t *testing.T) {
	issue := &domain.Issue{
		ID:         "id1",
		Identifier: "P-1",
		Title:      "Test",
		State:      "Todo",
		Labels:     []string{},
		BlockedBy:  []domain.BlockerRef{},
	}
	tmpl := "{% for x in %}"
	_, err := BuildPrompt(tmpl, issue, nil)
	if err == nil {
		t.Fatal("expected error for syntax error")
	}
}

func TestDefaultPromptBuilder(t *testing.T) {
	pb := NewDefaultPromptBuilder("Hello {{ issue.identifier }}")
	issue := &domain.Issue{
		ID:         "id1",
		Identifier: "P-1",
		Title:      "Test",
		State:      "Todo",
		Labels:     []string{},
		BlockedBy:  []domain.BlockerRef{},
	}
	result, err := pb.BuildPrompt(issue, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Hello P-1" {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestDefaultPromptBuilder_UpdateTemplate(t *testing.T) {
	pb := NewDefaultPromptBuilder("old {{ issue.identifier }}")
	pb.UpdateTemplate("new {{ issue.title }}")

	issue := &domain.Issue{
		ID:         "id1",
		Identifier: "P-1",
		Title:      "MyTitle",
		State:      "Todo",
		Labels:     []string{},
		BlockedBy:  []domain.BlockerRef{},
	}
	result, err := pb.BuildPrompt(issue, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "new MyTitle" {
		t.Errorf("unexpected result: %s", result)
	}
}
