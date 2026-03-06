package config

import (
	"os/user"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// Defaults
// ---------------------------------------------------------------------------

func TestParseServiceConfig_DefaultsApplyWhenEmpty(t *testing.T) {
	cfg := ParseServiceConfig(nil)

	if cfg.Polling.IntervalMs != DefaultPollingIntervalMs {
		t.Errorf("Polling.IntervalMs = %d, want %d", cfg.Polling.IntervalMs, DefaultPollingIntervalMs)
	}
	if cfg.Workspace.Root != DefaultWorkspaceRoot() {
		t.Errorf("Workspace.Root = %q, want %q", cfg.Workspace.Root, DefaultWorkspaceRoot())
	}
	if cfg.Hooks.TimeoutMs != DefaultHooksTimeoutMs {
		t.Errorf("Hooks.TimeoutMs = %d, want %d", cfg.Hooks.TimeoutMs, DefaultHooksTimeoutMs)
	}
	if cfg.Agent.MaxConcurrentAgents != DefaultMaxConcurrent {
		t.Errorf("Agent.MaxConcurrentAgents = %d, want %d", cfg.Agent.MaxConcurrentAgents, DefaultMaxConcurrent)
	}
	if cfg.Agent.MaxTurns != DefaultMaxTurns {
		t.Errorf("Agent.MaxTurns = %d, want %d", cfg.Agent.MaxTurns, DefaultMaxTurns)
	}
	if cfg.Agent.MaxRetryBackoffMs != DefaultMaxRetryBackoffMs {
		t.Errorf("Agent.MaxRetryBackoffMs = %d, want %d", cfg.Agent.MaxRetryBackoffMs, DefaultMaxRetryBackoffMs)
	}
	if cfg.Codex.Command != DefaultCodexCommand {
		t.Errorf("Codex.Command = %q, want %q", cfg.Codex.Command, DefaultCodexCommand)
	}
	if cfg.Codex.TurnTimeoutMs != DefaultTurnTimeoutMs {
		t.Errorf("Codex.TurnTimeoutMs = %d, want %d", cfg.Codex.TurnTimeoutMs, DefaultTurnTimeoutMs)
	}
	if cfg.Codex.ReadTimeoutMs != DefaultReadTimeoutMs {
		t.Errorf("Codex.ReadTimeoutMs = %d, want %d", cfg.Codex.ReadTimeoutMs, DefaultReadTimeoutMs)
	}
	if cfg.Codex.StallTimeoutMs != DefaultStallTimeoutMs {
		t.Errorf("Codex.StallTimeoutMs = %d, want %d", cfg.Codex.StallTimeoutMs, DefaultStallTimeoutMs)
	}
	if cfg.Server.Port != nil {
		t.Errorf("Server.Port = %v, want nil", cfg.Server.Port)
	}
	assertStringSliceEqual(t, "Tracker.ActiveStates", cfg.Tracker.ActiveStates, DefaultActiveStates)
	assertStringSliceEqual(t, "Tracker.TerminalStates", cfg.Tracker.TerminalStates, DefaultTerminalStates)
}

func TestParseServiceConfig_DefaultsWithEmptyMap(t *testing.T) {
	cfg := ParseServiceConfig(map[string]interface{}{})
	if cfg.Codex.Command != DefaultCodexCommand {
		t.Errorf("Codex.Command = %q, want %q", cfg.Codex.Command, DefaultCodexCommand)
	}
	if cfg.Workspace.Root != DefaultWorkspaceRoot() {
		t.Errorf("Workspace.Root = %q, want %q", cfg.Workspace.Root, DefaultWorkspaceRoot())
	}
}

// ---------------------------------------------------------------------------
// Tracker kind validation
// ---------------------------------------------------------------------------

func TestTrackerKindValidation(t *testing.T) {
	tests := []struct {
		name    string
		kind    string
		wantErr bool
	}{
		{"linear is valid", "linear", false},
		{"Linear mixed case is valid", "Linear", false},
		{"LINEAR uppercase is valid", "LINEAR", false},
		{"jira is not supported", "jira", true},
		{"empty kind is error", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &ServiceConfig{
				Tracker: TrackerConfig{
					Kind:        tt.kind,
					APIKey:      "test-key",
					ProjectSlug: "proj",
				},
				Codex: CodexConfig{Command: "codex"},
			}
			err := ValidateDispatchConfig(cfg)
			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tracker API key with $VAR indirection
// ---------------------------------------------------------------------------

func TestTrackerAPIKey_EnvVarIndirection(t *testing.T) {
	t.Setenv("TEST_LINEAR_KEY", "lin_secret_123")

	raw := map[string]interface{}{
		"tracker": map[string]interface{}{
			"kind":         "linear",
			"api_key":      "$TEST_LINEAR_KEY",
			"project_slug": "my-project",
		},
	}
	cfg := ParseServiceConfig(raw)

	if cfg.Tracker.APIKey != "lin_secret_123" {
		t.Errorf("Tracker.APIKey = %q, want %q", cfg.Tracker.APIKey, "lin_secret_123")
	}
}

func TestTrackerAPIKey_LiteralValue(t *testing.T) {
	raw := map[string]interface{}{
		"tracker": map[string]interface{}{
			"kind":         "linear",
			"api_key":      "literal-key-value",
			"project_slug": "my-project",
		},
	}
	cfg := ParseServiceConfig(raw)

	if cfg.Tracker.APIKey != "literal-key-value" {
		t.Errorf("Tracker.APIKey = %q, want %q", cfg.Tracker.APIKey, "literal-key-value")
	}
}

func TestTrackerAPIKey_UnsetEnvVar(t *testing.T) {
	// Ensure the env var is not set.
	t.Setenv("UNSET_VAR_TEST", "")

	raw := map[string]interface{}{
		"tracker": map[string]interface{}{
			"kind":    "linear",
			"api_key": "$UNSET_VAR_TEST",
		},
	}
	cfg := ParseServiceConfig(raw)

	if cfg.Tracker.APIKey != "" {
		t.Errorf("Tracker.APIKey = %q, want empty string", cfg.Tracker.APIKey)
	}
}

// ---------------------------------------------------------------------------
// $VAR resolution for general string fields
// ---------------------------------------------------------------------------

func TestEnvVarResolution_GeneralStrings(t *testing.T) {
	t.Setenv("MY_HOOK_CMD", "make setup")

	raw := map[string]interface{}{
		"hooks": map[string]interface{}{
			"after_create": "$MY_HOOK_CMD",
		},
	}
	cfg := ParseServiceConfig(raw)

	if cfg.Hooks.AfterCreate != "make setup" {
		t.Errorf("Hooks.AfterCreate = %q, want %q", cfg.Hooks.AfterCreate, "make setup")
	}
}

// ---------------------------------------------------------------------------
// ~ path expansion
// ---------------------------------------------------------------------------

func TestTildePathExpansion(t *testing.T) {
	u, err := user.Current()
	if err != nil {
		t.Skipf("cannot determine current user: %v", err)
	}

	raw := map[string]interface{}{
		"workspace": map[string]interface{}{
			"root": "~/symphony_ws",
		},
	}
	cfg := ParseServiceConfig(raw)

	want := filepath.Join(u.HomeDir, "symphony_ws")
	if cfg.Workspace.Root != want {
		t.Errorf("Workspace.Root = %q, want %q", cfg.Workspace.Root, want)
	}
}

func TestTildePathExpansion_JustTilde(t *testing.T) {
	u, err := user.Current()
	if err != nil {
		t.Skipf("cannot determine current user: %v", err)
	}

	raw := map[string]interface{}{
		"workspace": map[string]interface{}{
			"root": "~",
		},
	}
	cfg := ParseServiceConfig(raw)

	if cfg.Workspace.Root != u.HomeDir {
		t.Errorf("Workspace.Root = %q, want %q", cfg.Workspace.Root, u.HomeDir)
	}
}

func TestPathExpansion_EnvVarThenTilde(t *testing.T) {
	t.Setenv("MY_WS_PATH", "~/custom_ws")
	u, err := user.Current()
	if err != nil {
		t.Skipf("cannot determine current user: %v", err)
	}

	raw := map[string]interface{}{
		"workspace": map[string]interface{}{
			"root": "$MY_WS_PATH",
		},
	}
	cfg := ParseServiceConfig(raw)

	want := filepath.Join(u.HomeDir, "custom_ws")
	if cfg.Workspace.Root != want {
		t.Errorf("Workspace.Root = %q, want %q", cfg.Workspace.Root, want)
	}
}

// ---------------------------------------------------------------------------
// Codex command preserved as shell command string
// ---------------------------------------------------------------------------

func TestCodexCommandPreserved(t *testing.T) {
	raw := map[string]interface{}{
		"codex": map[string]interface{}{
			"command": "npx codex --full-auto --model o4-mini",
		},
	}
	cfg := ParseServiceConfig(raw)

	want := "npx codex --full-auto --model o4-mini"
	if cfg.Codex.Command != want {
		t.Errorf("Codex.Command = %q, want %q", cfg.Codex.Command, want)
	}
}

// ---------------------------------------------------------------------------
// Per-state concurrency override map
// ---------------------------------------------------------------------------

func TestPerStateConcurrency_NormalizesKeys(t *testing.T) {
	raw := map[string]interface{}{
		"agent": map[string]interface{}{
			"max_concurrent_agents_by_state": map[string]interface{}{
				"In Progress": 5,
				"  TODO  ":    3,
				"Review":      2,
			},
		},
	}
	cfg := ParseServiceConfig(raw)

	m := cfg.Agent.MaxConcurrentAgentsByState

	if v, ok := m["in progress"]; !ok || v != 5 {
		t.Errorf("expected key 'in progress' = 5, got %v (ok=%v)", v, ok)
	}
	if v, ok := m["todo"]; !ok || v != 3 {
		t.Errorf("expected key 'todo' = 3, got %v (ok=%v)", v, ok)
	}
	if v, ok := m["review"]; !ok || v != 2 {
		t.Errorf("expected key 'review' = 2, got %v (ok=%v)", v, ok)
	}
}

func TestPerStateConcurrency_IgnoresInvalidValues(t *testing.T) {
	raw := map[string]interface{}{
		"agent": map[string]interface{}{
			"max_concurrent_agents_by_state": map[string]interface{}{
				"todo":   0,
				"review": -1,
				"done":   3,
			},
		},
	}
	cfg := ParseServiceConfig(raw)

	m := cfg.Agent.MaxConcurrentAgentsByState
	if _, ok := m["todo"]; ok {
		t.Error("expected key 'todo' to be absent (value 0 is invalid)")
	}
	if _, ok := m["review"]; ok {
		t.Error("expected key 'review' to be absent (value -1 is invalid)")
	}
	if v := m["done"]; v != 3 {
		t.Errorf("expected key 'done' = 3, got %d", v)
	}
}

func TestPerStateConcurrency_EmptyKeyIgnored(t *testing.T) {
	raw := map[string]interface{}{
		"agent": map[string]interface{}{
			"max_concurrent_agents_by_state": map[string]interface{}{
				"":     5,
				"  ":   3,
				"todo": 2,
			},
		},
	}
	cfg := ParseServiceConfig(raw)

	m := cfg.Agent.MaxConcurrentAgentsByState
	if len(m) != 1 {
		t.Errorf("expected 1 entry, got %d: %v", len(m), m)
	}
	if v := m["todo"]; v != 2 {
		t.Errorf("expected key 'todo' = 2, got %d", v)
	}
}

// ---------------------------------------------------------------------------
// ValidateDispatchConfig
// ---------------------------------------------------------------------------

func TestValidateDispatchConfig_Valid(t *testing.T) {
	cfg := &ServiceConfig{
		Tracker: TrackerConfig{
			Kind:        "linear",
			APIKey:      "test-key",
			ProjectSlug: "proj-123",
		},
		Codex: CodexConfig{Command: "codex app-server"},
	}
	if err := ValidateDispatchConfig(cfg); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateDispatchConfig_MissingKind(t *testing.T) {
	cfg := &ServiceConfig{
		Tracker: TrackerConfig{
			APIKey:      "key",
			ProjectSlug: "proj",
		},
		Codex: CodexConfig{Command: "codex"},
	}
	err := ValidateDispatchConfig(cfg)
	if err == nil {
		t.Fatal("expected error for missing tracker.kind")
	}
	assertContains(t, err.Error(), "tracker.kind is required")
}

func TestValidateDispatchConfig_UnsupportedKind(t *testing.T) {
	cfg := &ServiceConfig{
		Tracker: TrackerConfig{
			Kind:   "github",
			APIKey: "key",
		},
		Codex: CodexConfig{Command: "codex"},
	}
	err := ValidateDispatchConfig(cfg)
	if err == nil {
		t.Fatal("expected error for unsupported kind")
	}
	assertContains(t, err.Error(), "not supported")
}

func TestValidateDispatchConfig_MissingAPIKey(t *testing.T) {
	cfg := &ServiceConfig{
		Tracker: TrackerConfig{
			Kind:        "linear",
			ProjectSlug: "proj",
		},
		Codex: CodexConfig{Command: "codex"},
	}
	err := ValidateDispatchConfig(cfg)
	if err == nil {
		t.Fatal("expected error for missing api_key")
	}
	assertContains(t, err.Error(), "tracker.api_key is required")
}

func TestValidateDispatchConfig_MissingProjectSlugForLinear(t *testing.T) {
	cfg := &ServiceConfig{
		Tracker: TrackerConfig{
			Kind:   "linear",
			APIKey: "key",
		},
		Codex: CodexConfig{Command: "codex"},
	}
	err := ValidateDispatchConfig(cfg)
	if err == nil {
		t.Fatal("expected error for missing project_slug with linear")
	}
	assertContains(t, err.Error(), "tracker.project_slug is required")
}

func TestValidateDispatchConfig_EmptyCodexCommand(t *testing.T) {
	cfg := &ServiceConfig{
		Tracker: TrackerConfig{
			Kind:        "linear",
			APIKey:      "key",
			ProjectSlug: "proj",
		},
		Codex: CodexConfig{Command: ""},
	}
	err := ValidateDispatchConfig(cfg)
	if err == nil {
		t.Fatal("expected error for empty codex.command")
	}
	assertContains(t, err.Error(), "codex.command is required")
}

func TestValidateDispatchConfig_APIKeyFromUnsetEnvVar(t *testing.T) {
	t.Setenv("VALIDATE_UNSET_KEY", "")

	raw := map[string]interface{}{
		"tracker": map[string]interface{}{
			"kind":         "linear",
			"api_key":      "$VALIDATE_UNSET_KEY",
			"project_slug": "proj",
		},
	}
	cfg := ParseServiceConfig(raw)
	err := ValidateDispatchConfig(cfg)
	if err == nil {
		t.Fatal("expected error when $VAR resolves to empty string")
	}
	assertContains(t, err.Error(), "tracker.api_key is required")
}

// ---------------------------------------------------------------------------
// Numeric fields accept int and string int
// ---------------------------------------------------------------------------

func TestNumericFields_IntAndStringInt(t *testing.T) {
	raw := map[string]interface{}{
		"polling": map[string]interface{}{
			"interval_ms": 5000,
		},
	}
	cfg := ParseServiceConfig(raw)
	if cfg.Polling.IntervalMs != 5000 {
		t.Errorf("IntervalMs = %d, want 5000", cfg.Polling.IntervalMs)
	}

	raw2 := map[string]interface{}{
		"polling": map[string]interface{}{
			"interval_ms": "7000",
		},
	}
	cfg2 := ParseServiceConfig(raw2)
	if cfg2.Polling.IntervalMs != 7000 {
		t.Errorf("IntervalMs = %d, want 7000", cfg2.Polling.IntervalMs)
	}
}

func TestNumericFields_Float64(t *testing.T) {
	raw := map[string]interface{}{
		"polling": map[string]interface{}{
			"interval_ms": float64(15000),
		},
	}
	cfg := ParseServiceConfig(raw)
	if cfg.Polling.IntervalMs != 15000 {
		t.Errorf("IntervalMs = %d, want 15000", cfg.Polling.IntervalMs)
	}
}

func TestNumericFields_InvalidStringFallsBackToDefault(t *testing.T) {
	raw := map[string]interface{}{
		"polling": map[string]interface{}{
			"interval_ms": "not-a-number",
		},
	}
	cfg := ParseServiceConfig(raw)
	if cfg.Polling.IntervalMs != DefaultPollingIntervalMs {
		t.Errorf("IntervalMs = %d, want default %d", cfg.Polling.IntervalMs, DefaultPollingIntervalMs)
	}
}

func TestNumericFields_StringIntWithEnvVar(t *testing.T) {
	t.Setenv("TEST_INTERVAL", "9000")
	raw := map[string]interface{}{
		"polling": map[string]interface{}{
			"interval_ms": "$TEST_INTERVAL",
		},
	}
	cfg := ParseServiceConfig(raw)
	if cfg.Polling.IntervalMs != 9000 {
		t.Errorf("IntervalMs = %d, want 9000", cfg.Polling.IntervalMs)
	}
}

// ---------------------------------------------------------------------------
// active_states and terminal_states: list and comma-separated string
// ---------------------------------------------------------------------------

func TestActiveStates_AsList(t *testing.T) {
	raw := map[string]interface{}{
		"tracker": map[string]interface{}{
			"active_states": []interface{}{"Ready", "Working"},
		},
	}
	cfg := ParseServiceConfig(raw)
	assertStringSliceEqual(t, "ActiveStates", cfg.Tracker.ActiveStates, []string{"Ready", "Working"})
}

func TestActiveStates_AsCommaSeparatedString(t *testing.T) {
	raw := map[string]interface{}{
		"tracker": map[string]interface{}{
			"active_states": "Ready, Working, Blocked",
		},
	}
	cfg := ParseServiceConfig(raw)
	assertStringSliceEqual(t, "ActiveStates", cfg.Tracker.ActiveStates, []string{"Ready", "Working", "Blocked"})
}

func TestTerminalStates_AsList(t *testing.T) {
	raw := map[string]interface{}{
		"tracker": map[string]interface{}{
			"terminal_states": []interface{}{"Done", "Archived"},
		},
	}
	cfg := ParseServiceConfig(raw)
	assertStringSliceEqual(t, "TerminalStates", cfg.Tracker.TerminalStates, []string{"Done", "Archived"})
}

func TestTerminalStates_AsCommaSeparatedString(t *testing.T) {
	raw := map[string]interface{}{
		"tracker": map[string]interface{}{
			"terminal_states": "Done, Archived",
		},
	}
	cfg := ParseServiceConfig(raw)
	assertStringSliceEqual(t, "TerminalStates", cfg.Tracker.TerminalStates, []string{"Done", "Archived"})
}

func TestStates_EmptyFallsBackToDefaults(t *testing.T) {
	raw := map[string]interface{}{
		"tracker": map[string]interface{}{
			"active_states":   []interface{}{},
			"terminal_states": "",
		},
	}
	cfg := ParseServiceConfig(raw)
	assertStringSliceEqual(t, "ActiveStates", cfg.Tracker.ActiveStates, DefaultActiveStates)
	assertStringSliceEqual(t, "TerminalStates", cfg.Tracker.TerminalStates, DefaultTerminalStates)
}

// ---------------------------------------------------------------------------
// Linear endpoint default
// ---------------------------------------------------------------------------

func TestLinearEndpointDefault(t *testing.T) {
	raw := map[string]interface{}{
		"tracker": map[string]interface{}{
			"kind": "linear",
		},
	}
	cfg := ParseServiceConfig(raw)
	if cfg.Tracker.Endpoint != DefaultLinearEndpoint {
		t.Errorf("Tracker.Endpoint = %q, want %q", cfg.Tracker.Endpoint, DefaultLinearEndpoint)
	}
}

func TestLinearEndpointCustom(t *testing.T) {
	raw := map[string]interface{}{
		"tracker": map[string]interface{}{
			"kind":     "linear",
			"endpoint": "https://custom.linear.dev/graphql",
		},
	}
	cfg := ParseServiceConfig(raw)
	if cfg.Tracker.Endpoint != "https://custom.linear.dev/graphql" {
		t.Errorf("Tracker.Endpoint = %q, want custom endpoint", cfg.Tracker.Endpoint)
	}
}

// ---------------------------------------------------------------------------
// Server port
// ---------------------------------------------------------------------------

func TestServerPort_Set(t *testing.T) {
	raw := map[string]interface{}{
		"server": map[string]interface{}{
			"port": 8080,
		},
	}
	cfg := ParseServiceConfig(raw)
	if cfg.Server.Port == nil {
		t.Fatal("Server.Port is nil, want 8080")
	}
	if *cfg.Server.Port != 8080 {
		t.Errorf("Server.Port = %d, want 8080", *cfg.Server.Port)
	}
}

func TestServerPort_NotSet(t *testing.T) {
	cfg := ParseServiceConfig(map[string]interface{}{})
	if cfg.Server.Port != nil {
		t.Errorf("Server.Port = %v, want nil", cfg.Server.Port)
	}
}

// ---------------------------------------------------------------------------
// Hooks negative timeout defaults
// ---------------------------------------------------------------------------

func TestHooksTimeout_NegativeDefaultsToDefault(t *testing.T) {
	raw := map[string]interface{}{
		"hooks": map[string]interface{}{
			"timeout_ms": -100,
		},
	}
	cfg := ParseServiceConfig(raw)
	if cfg.Hooks.TimeoutMs != DefaultHooksTimeoutMs {
		t.Errorf("Hooks.TimeoutMs = %d, want %d", cfg.Hooks.TimeoutMs, DefaultHooksTimeoutMs)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func assertStringSliceEqual(t *testing.T, name string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s length = %d, want %d; got %v, want %v", name, len(got), len(want), got, want)
		return
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("%s[%d] = %q, want %q", name, i, got[i], want[i])
		}
	}
}

func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if !contains(s, substr) {
		t.Errorf("expected %q to contain %q", s, substr)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
