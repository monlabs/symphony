package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// Missing file
// ---------------------------------------------------------------------------

func TestLoadWorkflow_MissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")

	_, err := LoadWorkflow(path)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !errors.Is(err, ErrMissingWorkflowFile) {
		t.Errorf("expected ErrMissingWorkflowFile, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Valid YAML front matter
// ---------------------------------------------------------------------------

func TestLoadWorkflow_ValidFrontMatter(t *testing.T) {
	content := `---
tracker:
  kind: linear
  api_key: test-key
  project_slug: my-project
polling:
  interval_ms: 5000
---
You are a coding agent. Fix the issue described below.

Issue: {{.Title}}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	wd, err := LoadWorkflow(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check that config map has expected keys.
	if _, ok := wd.Config["tracker"]; !ok {
		t.Error("expected 'tracker' key in Config")
	}
	if _, ok := wd.Config["polling"]; !ok {
		t.Error("expected 'polling' key in Config")
	}

	// Verify nested values.
	tracker, ok := wd.Config["tracker"].(map[string]interface{})
	if !ok {
		t.Fatalf("tracker is not a map, got %T", wd.Config["tracker"])
	}
	if tracker["kind"] != "linear" {
		t.Errorf("tracker.kind = %v, want 'linear'", tracker["kind"])
	}
	if tracker["api_key"] != "test-key" {
		t.Errorf("tracker.api_key = %v, want 'test-key'", tracker["api_key"])
	}

	// Check prompt body.
	wantPromptPrefix := "You are a coding agent."
	if len(wd.PromptTemplate) < len(wantPromptPrefix) || wd.PromptTemplate[:len(wantPromptPrefix)] != wantPromptPrefix {
		t.Errorf("PromptTemplate does not start with expected prefix, got: %q", wd.PromptTemplate)
	}
}

// ---------------------------------------------------------------------------
// Invalid YAML front matter
// ---------------------------------------------------------------------------

func TestLoadWorkflow_InvalidYAML(t *testing.T) {
	content := `---
tracker:
  kind: [invalid yaml
  broken: {
---
Some prompt body.
`
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadWorkflow(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}

	var parseErr *ErrWorkflowParseError
	if !errors.As(err, &parseErr) {
		t.Errorf("expected ErrWorkflowParseError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Non-map front matter
// ---------------------------------------------------------------------------

func TestLoadWorkflow_NonMapFrontMatter(t *testing.T) {
	content := `---
- item1
- item2
- item3
---
Some prompt body.
`
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadWorkflow(path)
	if err == nil {
		t.Fatal("expected error for non-map front matter")
	}
	if !errors.Is(err, ErrFrontMatterNotAMap) {
		t.Errorf("expected ErrFrontMatterNotAMap, got %T: %v", err, err)
	}
}

func TestLoadWorkflow_ScalarFrontMatter(t *testing.T) {
	content := `---
just a plain string
---
Some prompt body.
`
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadWorkflow(path)
	if err == nil {
		t.Fatal("expected error for scalar front matter")
	}
	if !errors.Is(err, ErrFrontMatterNotAMap) {
		t.Errorf("expected ErrFrontMatterNotAMap, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// No front matter
// ---------------------------------------------------------------------------

func TestLoadWorkflow_NoFrontMatter(t *testing.T) {
	content := `You are a coding agent. Fix the issue.

Issue: {{.Title}}
Description: {{.Description}}`

	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	wd, err := LoadWorkflow(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(wd.Config) != 0 {
		t.Errorf("expected empty Config map, got %v", wd.Config)
	}

	want := "You are a coding agent. Fix the issue.\n\nIssue: {{.Title}}\nDescription: {{.Description}}"
	if wd.PromptTemplate != want {
		t.Errorf("PromptTemplate = %q, want %q", wd.PromptTemplate, want)
	}
}

// ---------------------------------------------------------------------------
// Prompt body is trimmed
// ---------------------------------------------------------------------------

func TestLoadWorkflow_PromptBodyTrimmed(t *testing.T) {
	content := `---
tracker:
  kind: linear
---


  Hello, I am a prompt.

`
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	wd, err := LoadWorkflow(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if wd.PromptTemplate != "Hello, I am a prompt." {
		t.Errorf("PromptTemplate = %q, want %q", wd.PromptTemplate, "Hello, I am a prompt.")
	}
}

func TestLoadWorkflow_NoFrontMatter_PromptBodyTrimmed(t *testing.T) {
	content := `

   Hello, world!

`
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	wd, err := LoadWorkflow(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if wd.PromptTemplate != "Hello, world!" {
		t.Errorf("PromptTemplate = %q, want %q", wd.PromptTemplate, "Hello, world!")
	}
}

// ---------------------------------------------------------------------------
// Empty front matter
// ---------------------------------------------------------------------------

func TestLoadWorkflow_EmptyFrontMatter(t *testing.T) {
	content := `---
---
The prompt body here.
`
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	wd, err := LoadWorkflow(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(wd.Config) != 0 {
		t.Errorf("expected empty Config map for empty front matter, got %v", wd.Config)
	}

	if wd.PromptTemplate != "The prompt body here." {
		t.Errorf("PromptTemplate = %q, want %q", wd.PromptTemplate, "The prompt body here.")
	}
}

func TestLoadWorkflow_EmptyFrontMatterWithWhitespace(t *testing.T) {
	content := `---

---
Body text.
`
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	wd, err := LoadWorkflow(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(wd.Config) != 0 {
		t.Errorf("expected empty Config map, got %v", wd.Config)
	}
}

// ---------------------------------------------------------------------------
// Opening --- without closing ---
// ---------------------------------------------------------------------------

func TestLoadWorkflow_OpeningWithoutClosing(t *testing.T) {
	content := `---
tracker:
  kind: linear
  api_key: test-key
This content never has a closing delimiter.
`
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadWorkflow(path)
	if err == nil {
		t.Fatal("expected error for opening --- without closing ---")
	}

	var parseErr *ErrWorkflowParseError
	if !errors.As(err, &parseErr) {
		t.Errorf("expected ErrWorkflowParseError, got %T: %v", err, err)
	}
	if parseErr.Cause == nil || !containsStr(parseErr.Cause.Error(), "no closing ---") {
		t.Errorf("expected cause to mention 'no closing ---', got: %v", parseErr.Cause)
	}
}

// ---------------------------------------------------------------------------
// Integration: LoadWorkflow then ParseServiceConfig
// ---------------------------------------------------------------------------

func TestLoadWorkflow_IntegrationWithParseServiceConfig(t *testing.T) {
	t.Setenv("TEST_INTEGRATION_KEY", "lin_integration_abc")

	content := `---
tracker:
  kind: linear
  api_key: $TEST_INTEGRATION_KEY
  project_slug: test-proj
codex:
  command: "npx codex --full-auto"
agent:
  max_concurrent_agents: 5
---
Fix the bug described below.
`
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	wd, err := LoadWorkflow(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg := ParseServiceConfig(wd.Config)

	if cfg.Tracker.Kind != "linear" {
		t.Errorf("Tracker.Kind = %q, want 'linear'", cfg.Tracker.Kind)
	}
	if cfg.Tracker.APIKey != "lin_integration_abc" {
		t.Errorf("Tracker.APIKey = %q, want 'lin_integration_abc'", cfg.Tracker.APIKey)
	}
	if cfg.Tracker.ProjectSlug != "test-proj" {
		t.Errorf("Tracker.ProjectSlug = %q, want 'test-proj'", cfg.Tracker.ProjectSlug)
	}
	if cfg.Codex.Command != "npx codex --full-auto" {
		t.Errorf("Codex.Command = %q, want 'npx codex --full-auto'", cfg.Codex.Command)
	}
	if cfg.Agent.MaxConcurrentAgents != 5 {
		t.Errorf("Agent.MaxConcurrentAgents = %d, want 5", cfg.Agent.MaxConcurrentAgents)
	}

	if err := ValidateDispatchConfig(cfg); err != nil {
		t.Errorf("ValidateDispatchConfig returned unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Edge: file with only front matter delimiters and no body
// ---------------------------------------------------------------------------

func TestLoadWorkflow_OnlyDelimitersNoBody(t *testing.T) {
	content := `---
tracker:
  kind: linear
---`
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	wd, err := LoadWorkflow(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if wd.PromptTemplate != "" {
		t.Errorf("PromptTemplate = %q, want empty string", wd.PromptTemplate)
	}

	if _, ok := wd.Config["tracker"]; !ok {
		t.Error("expected 'tracker' key in Config")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
