// Package config implements the typed configuration layer for Symphony (Spec Section 6).
// It parses WORKFLOW.md YAML front matter into typed runtime settings, applies defaults,
// resolves environment variable indirection, and validates dispatch prerequisites.
package config

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// Config structs
// ---------------------------------------------------------------------------

// ServiceConfig holds all typed runtime settings derived from WORKFLOW.md front matter.
type ServiceConfig struct {
	Tracker   TrackerConfig
	Polling   PollingConfig
	Workspace WorkspaceConfig
	Hooks     HooksConfig
	Agent     AgentConfig
	Codex     CodexConfig
	Server    ServerConfig
}

// TrackerConfig configures the issue-tracker integration.
type TrackerConfig struct {
	Kind           string
	Endpoint       string
	APIKey         string
	ProjectSlug    string
	ActiveStates   []string
	TerminalStates []string
}

// PollingConfig configures the poll cadence.
type PollingConfig struct {
	IntervalMs int
}

// WorkspaceConfig configures workspace paths.
type WorkspaceConfig struct {
	Root string
}

// HooksConfig configures workspace lifecycle hooks.
type HooksConfig struct {
	AfterCreate  string
	BeforeRun    string
	AfterRun     string
	BeforeRemove string
	TimeoutMs    int
}

// AgentConfig configures agent concurrency and retry limits.
type AgentConfig struct {
	MaxConcurrentAgents        int
	MaxTurns                   int
	MaxRetryBackoffMs          int
	MaxConcurrentAgentsByState map[string]int
}

// CodexConfig configures the coding-agent subprocess.
type CodexConfig struct {
	Command           string
	ApprovalPolicy    string
	ThreadSandbox     string
	TurnSandboxPolicy string
	TurnTimeoutMs     int
	ReadTimeoutMs     int
	StallTimeoutMs    int
}

// ServerConfig configures the optional HTTP server extension.
type ServerConfig struct {
	Port *int
}

// ---------------------------------------------------------------------------
// Defaults
// ---------------------------------------------------------------------------

const (
	DefaultLinearEndpoint    = "https://api.linear.app/graphql"
	DefaultPollingIntervalMs = 30000
	DefaultHooksTimeoutMs    = 60000
	DefaultMaxConcurrent     = 10
	DefaultMaxTurns          = 20
	DefaultMaxRetryBackoffMs = 300000
	DefaultCodexCommand        = "codex app-server"
	DefaultApprovalPolicy      = "auto-edit"
	DefaultThreadSandbox       = "workspace-write"
	DefaultTurnSandboxPolicy   = "workspace-write"
	DefaultTurnTimeoutMs       = 3600000
	DefaultReadTimeoutMs       = 5000
	DefaultStallTimeoutMs      = 300000
)

var (
	DefaultActiveStates   = []string{"Todo", "In Progress"}
	DefaultTerminalStates = []string{"Closed", "Cancelled", "Canceled", "Duplicate", "Done"}
)

// DefaultWorkspaceRoot returns the platform-specific default workspace root.
func DefaultWorkspaceRoot() string {
	return filepath.Join(os.TempDir(), "symphony_workspaces")
}

// ---------------------------------------------------------------------------
// ParseServiceConfig
// ---------------------------------------------------------------------------

// ParseServiceConfig extracts typed configuration from raw YAML front matter.
// Missing keys fall back to built-in defaults. Environment variable indirection
// ($VAR) is resolved for string values, and ~ is expanded for path fields.
func ParseServiceConfig(raw map[string]interface{}) *ServiceConfig {
	cfg := &ServiceConfig{}

	tracker := subMap(raw, "tracker")
	cfg.Tracker.Kind = getString(tracker, "kind")
	cfg.Tracker.Endpoint = getString(tracker, "endpoint")
	if cfg.Tracker.Endpoint == "" && strings.EqualFold(cfg.Tracker.Kind, "linear") {
		cfg.Tracker.Endpoint = DefaultLinearEndpoint
	}
	cfg.Tracker.APIKey = resolveEnv(getString(tracker, "api_key"))
	cfg.Tracker.ProjectSlug = getString(tracker, "project_slug")
	cfg.Tracker.ActiveStates = getStringList(tracker, "active_states")
	if len(cfg.Tracker.ActiveStates) == 0 {
		cfg.Tracker.ActiveStates = DefaultActiveStates
	}
	cfg.Tracker.TerminalStates = getStringList(tracker, "terminal_states")
	if len(cfg.Tracker.TerminalStates) == 0 {
		cfg.Tracker.TerminalStates = DefaultTerminalStates
	}

	polling := subMap(raw, "polling")
	cfg.Polling.IntervalMs = getInt(polling, "interval_ms", DefaultPollingIntervalMs)

	workspace := subMap(raw, "workspace")
	cfg.Workspace.Root = expandPath(getString(workspace, "root"))
	if cfg.Workspace.Root == "" {
		cfg.Workspace.Root = DefaultWorkspaceRoot()
	}

	hooks := subMap(raw, "hooks")
	cfg.Hooks.AfterCreate = getString(hooks, "after_create")
	cfg.Hooks.BeforeRun = getString(hooks, "before_run")
	cfg.Hooks.AfterRun = getString(hooks, "after_run")
	cfg.Hooks.BeforeRemove = getString(hooks, "before_remove")
	cfg.Hooks.TimeoutMs = getInt(hooks, "timeout_ms", DefaultHooksTimeoutMs)
	if cfg.Hooks.TimeoutMs <= 0 {
		cfg.Hooks.TimeoutMs = DefaultHooksTimeoutMs
	}

	agent := subMap(raw, "agent")
	cfg.Agent.MaxConcurrentAgents = getInt(agent, "max_concurrent_agents", DefaultMaxConcurrent)
	cfg.Agent.MaxTurns = getInt(agent, "max_turns", DefaultMaxTurns)
	cfg.Agent.MaxRetryBackoffMs = getInt(agent, "max_retry_backoff_ms", DefaultMaxRetryBackoffMs)
	cfg.Agent.MaxConcurrentAgentsByState = getIntMap(agent, "max_concurrent_agents_by_state")

	codex := subMap(raw, "codex")
	cfg.Codex.Command = getString(codex, "command")
	if cfg.Codex.Command == "" {
		cfg.Codex.Command = DefaultCodexCommand
	}
	cfg.Codex.ApprovalPolicy = getString(codex, "approval_policy")
	if cfg.Codex.ApprovalPolicy == "" {
		cfg.Codex.ApprovalPolicy = DefaultApprovalPolicy
	}
	cfg.Codex.ThreadSandbox = getString(codex, "thread_sandbox")
	if cfg.Codex.ThreadSandbox == "" {
		cfg.Codex.ThreadSandbox = DefaultThreadSandbox
	}
	cfg.Codex.TurnSandboxPolicy = getString(codex, "turn_sandbox_policy")
	if cfg.Codex.TurnSandboxPolicy == "" {
		cfg.Codex.TurnSandboxPolicy = DefaultTurnSandboxPolicy
	}
	cfg.Codex.TurnTimeoutMs = getInt(codex, "turn_timeout_ms", DefaultTurnTimeoutMs)
	cfg.Codex.ReadTimeoutMs = getInt(codex, "read_timeout_ms", DefaultReadTimeoutMs)
	cfg.Codex.StallTimeoutMs = getInt(codex, "stall_timeout_ms", DefaultStallTimeoutMs)

	server := subMap(raw, "server")
	if v, ok := server["port"]; ok && v != nil {
		p := toInt(v)
		cfg.Server.Port = &p
	}

	return cfg
}

// ---------------------------------------------------------------------------
// ValidateDispatchConfig – Spec Section 6.3 preflight checks
// ---------------------------------------------------------------------------

// ValidateDispatchConfig performs dispatch preflight validation per Spec Section 6.3.
// It returns a non-nil error if the configuration is not sufficient to begin dispatching work.
func ValidateDispatchConfig(cfg *ServiceConfig) error {
	if cfg.Tracker.Kind == "" {
		return fmt.Errorf("tracker.kind is required")
	}
	supportedKinds := map[string]bool{"linear": true}
	if !supportedKinds[strings.ToLower(cfg.Tracker.Kind)] {
		return fmt.Errorf("tracker.kind %q is not supported (supported: linear)", cfg.Tracker.Kind)
	}
	if cfg.Tracker.APIKey == "" {
		return fmt.Errorf("tracker.api_key is required (after $VAR resolution)")
	}
	if strings.EqualFold(cfg.Tracker.Kind, "linear") && cfg.Tracker.ProjectSlug == "" {
		return fmt.Errorf("tracker.project_slug is required when tracker.kind=linear")
	}
	if cfg.Codex.Command == "" {
		return fmt.Errorf("codex.command is required and must be non-empty")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Helpers: map navigation
// ---------------------------------------------------------------------------

// subMap extracts a nested map from a parent map. Returns empty map if missing or wrong type.
func subMap(m map[string]interface{}, key string) map[string]interface{} {
	if m == nil {
		return map[string]interface{}{}
	}
	v, ok := m[key]
	if !ok || v == nil {
		return map[string]interface{}{}
	}
	switch t := v.(type) {
	case map[string]interface{}:
		return t
	case map[interface{}]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, val := range t {
			out[fmt.Sprintf("%v", k)] = val
		}
		return out
	default:
		return map[string]interface{}{}
	}
}

// getString retrieves a string value from a map, resolving $VAR env indirection
// for values that start with $.
func getString(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	s := fmt.Sprintf("%v", v)
	return resolveEnv(s)
}

// resolveEnv resolves $VAR-style environment variable indirection.
// If the string starts with $, the remainder is used as an env var name.
// If the env var is empty, the result is the empty string.
func resolveEnv(s string) string {
	if strings.HasPrefix(s, "$") {
		varName := s[1:]
		return os.Getenv(varName)
	}
	return s
}

// expandPath expands ~ to the user's home directory in path strings.
func expandPath(s string) string {
	if s == "" {
		return s
	}
	s = resolveEnv(s)
	if s == "~" || strings.HasPrefix(s, "~/") {
		if u, err := user.Current(); err == nil {
			s = filepath.Join(u.HomeDir, s[1:])
		}
	}
	return s
}

// getInt retrieves an integer value from a map, accepting both int and string representations.
// Returns the provided default if the key is missing or cannot be parsed.
func getInt(m map[string]interface{}, key string, def int) int {
	if m == nil {
		return def
	}
	v, ok := m[key]
	if !ok || v == nil {
		return def
	}
	return toIntOr(v, def)
}

// toInt converts a value to int, returning 0 if conversion fails.
func toInt(v interface{}) int {
	return toIntOr(v, 0)
}

// toIntOr converts a value to int, returning def if conversion fails.
// Supports int, float64 (from JSON/YAML), and string representations.
func toIntOr(v interface{}, def int) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	case string:
		s := resolveEnv(t)
		if n, err := strconv.Atoi(s); err == nil {
			return n
		}
		return def
	default:
		return def
	}
}

// getStringList extracts a list of strings from a map value.
// Accepts either a YAML list of strings or a comma-separated string.
func getStringList(m map[string]interface{}, key string) []string {
	if m == nil {
		return nil
	}
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}

	switch t := v.(type) {
	case []interface{}:
		out := make([]string, 0, len(t))
		for _, item := range t {
			s := strings.TrimSpace(fmt.Sprintf("%v", item))
			if s != "" {
				out = append(out, s)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case string:
		return splitCommaSeparated(t)
	default:
		s := fmt.Sprintf("%v", v)
		return splitCommaSeparated(s)
	}
}

// splitCommaSeparated splits a comma-separated string into trimmed, non-empty parts.
func splitCommaSeparated(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// getIntMap extracts a map[string]int from a map value.
// Keys are normalized with trim+lowercase. Non-positive values are ignored.
func getIntMap(m map[string]interface{}, key string) map[string]int {
	if m == nil {
		return map[string]int{}
	}
	v, ok := m[key]
	if !ok || v == nil {
		return map[string]int{}
	}

	var raw map[string]interface{}
	switch t := v.(type) {
	case map[string]interface{}:
		raw = t
	case map[interface{}]interface{}:
		raw = make(map[string]interface{}, len(t))
		for k, val := range t {
			raw[fmt.Sprintf("%v", k)] = val
		}
	default:
		return map[string]int{}
	}

	out := make(map[string]int, len(raw))
	for k, val := range raw {
		normalizedKey := strings.ToLower(strings.TrimSpace(k))
		if normalizedKey == "" {
			continue
		}
		n := toInt(val)
		if n > 0 {
			out[normalizedKey] = n
		}
	}
	return out
}
