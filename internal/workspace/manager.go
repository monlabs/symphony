package workspace

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/monlabs/symphony/internal/domain"
)

// defaultHookTimeoutMs is the default timeout for hook execution if not configured.
const defaultHookTimeoutMs = 60000

// safeChars matches characters that are allowed in sanitized identifiers.
var safeChars = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// HooksConfig holds workspace hook configuration.
type HooksConfig struct {
	AfterCreate  string
	BeforeRun    string
	AfterRun     string
	BeforeRemove string
	TimeoutMs    int
}

// Manager handles workspace lifecycle.
type Manager struct {
	root  string
	hooks HooksConfig
}

// NewManager creates a new workspace Manager with the given root directory and hooks config.
func NewManager(root string, hooks HooksConfig) *Manager {
	return &Manager{
		root:  root,
		hooks: hooks,
	}
}

// SanitizeIdentifier converts an issue identifier to a safe directory name.
// Only [A-Za-z0-9._-] are allowed; all other chars are replaced with _.
func SanitizeIdentifier(identifier string) string {
	return safeChars.ReplaceAllString(identifier, "_")
}

// WorkspacePath returns the full path for an issue workspace.
func (m *Manager) WorkspacePath(identifier string) string {
	return filepath.Join(m.root, SanitizeIdentifier(identifier))
}

// ValidateWorkspacePath checks that workspace path is under workspace root (safety invariant).
func (m *Manager) ValidateWorkspacePath(workspacePath string) error {
	absRoot, err := filepath.Abs(m.root)
	if err != nil {
		return fmt.Errorf("failed to resolve workspace root: %w", err)
	}
	absWorkspace, err := filepath.Abs(workspacePath)
	if err != nil {
		return fmt.Errorf("failed to resolve workspace path: %w", err)
	}

	// Ensure the workspace path is strictly under the root directory.
	// Add a trailing separator to the root so that "/workspace-foo" does not
	// falsely match a root of "/workspace".
	rootPrefix := absRoot + string(filepath.Separator)
	if !strings.HasPrefix(absWorkspace, rootPrefix) {
		return fmt.Errorf("workspace path %q is not under root %q", absWorkspace, absRoot)
	}
	return nil
}

// CreateForIssue creates or reuses a workspace for the given issue identifier.
// Returns the workspace info including whether it was newly created.
func (m *Manager) CreateForIssue(ctx context.Context, identifier string) (*domain.Workspace, error) {
	sanitized := SanitizeIdentifier(identifier)
	wsPath := filepath.Join(m.root, sanitized)

	if err := m.ValidateWorkspacePath(wsPath); err != nil {
		return nil, fmt.Errorf("workspace path validation failed: %w", err)
	}

	createdNow := false

	info, err := os.Stat(wsPath)
	if os.IsNotExist(err) {
		if mkErr := os.MkdirAll(wsPath, 0o755); mkErr != nil {
			return nil, fmt.Errorf("failed to create workspace directory: %w", mkErr)
		}
		createdNow = true
		slog.Info("created workspace directory", "path", wsPath, "identifier", identifier)
	} else if err != nil {
		return nil, fmt.Errorf("failed to stat workspace path: %w", err)
	} else if !info.IsDir() {
		return nil, fmt.Errorf("workspace path %q exists but is not a directory", wsPath)
	}

	// Run after_create hook if the workspace was just created.
	if createdNow && m.hooks.AfterCreate != "" {
		slog.Info("running after_create hook", "path", wsPath)
		if hookErr := m.runHook(ctx, wsPath, m.hooks.AfterCreate); hookErr != nil {
			slog.Error("after_create hook failed, removing workspace", "path", wsPath, "error", hookErr)
			_ = os.RemoveAll(wsPath)
			return nil, fmt.Errorf("after_create hook failed: %w", hookErr)
		}
	}

	return &domain.Workspace{
		Path:         wsPath,
		WorkspaceKey: sanitized,
		CreatedNow:   createdNow,
	}, nil
}

// RunBeforeRun executes the before_run hook in the workspace directory.
func (m *Manager) RunBeforeRun(ctx context.Context, workspacePath string) error {
	if m.hooks.BeforeRun == "" {
		return nil
	}
	slog.Info("running before_run hook", "path", workspacePath)
	if err := m.runHook(ctx, workspacePath, m.hooks.BeforeRun); err != nil {
		return fmt.Errorf("before_run hook failed: %w", err)
	}
	return nil
}

// RunAfterRun executes the after_run hook (best-effort, errors logged and ignored).
func (m *Manager) RunAfterRun(ctx context.Context, workspacePath string) {
	if m.hooks.AfterRun == "" {
		return
	}
	slog.Info("running after_run hook", "path", workspacePath)
	if err := m.runHook(ctx, workspacePath, m.hooks.AfterRun); err != nil {
		slog.Warn("after_run hook failed (ignored)", "path", workspacePath, "error", err)
	}
}

// CleanWorkspace removes a workspace directory, running before_remove hook first.
func (m *Manager) CleanWorkspace(ctx context.Context, identifier string) {
	wsPath := m.WorkspacePath(identifier)

	if _, err := os.Stat(wsPath); os.IsNotExist(err) {
		slog.Info("workspace directory does not exist, nothing to clean", "path", wsPath)
		return
	}

	// Run before_remove hook (best-effort).
	if m.hooks.BeforeRemove != "" {
		slog.Info("running before_remove hook", "path", wsPath)
		if err := m.runHook(ctx, wsPath, m.hooks.BeforeRemove); err != nil {
			slog.Warn("before_remove hook failed (ignored)", "path", wsPath, "error", err)
		}
	}

	slog.Info("removing workspace directory", "path", wsPath)
	if err := os.RemoveAll(wsPath); err != nil {
		slog.Error("failed to remove workspace directory", "path", wsPath, "error", err)
	}
}

// UpdateConfig updates the hooks config (for dynamic reload).
func (m *Manager) UpdateConfig(root string, hooks HooksConfig) {
	m.root = root
	m.hooks = hooks
}

// hookTimeout returns the configured hook timeout duration.
func (m *Manager) hookTimeout() time.Duration {
	ms := m.hooks.TimeoutMs
	if ms <= 0 {
		ms = defaultHookTimeoutMs
	}
	return time.Duration(ms) * time.Millisecond
}

// runHook executes a hook script via "bash -lc <script>" with the given directory as cwd.
func (m *Manager) runHook(ctx context.Context, dir string, script string) error {
	timeout := m.hookTimeout()
	hookCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(hookCtx, "bash", "-lc", script)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("hook %q in %q failed: %w", script, dir, err)
	}
	return nil
}
