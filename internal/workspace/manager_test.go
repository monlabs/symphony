package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// SanitizeIdentifier
// ---------------------------------------------------------------------------

func TestSanitizeIdentifier_ReplacesNonSafeChars(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "simple"},
		{"UPPER-lower.123", "UPPER-lower.123"},
		{"foo/bar", "foo_bar"},
		{"issue #42", "issue__42"},
		{"a@b$c%d^e&f", "a_b_c_d_e_f"},
		{"hello world!", "hello_world_"},
		{"proj/TEAM-123", "proj_TEAM-123"},
		{"café", "caf_"},          // non-ASCII replaced (é is one rune)
		{"a___b", "a___b"},        // underscores kept
		{"dots.and-dashes", "dots.and-dashes"},
		{"", ""},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := SanitizeIdentifier(tc.input)
			if got != tc.want {
				t.Errorf("SanitizeIdentifier(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Deterministic workspace path per identifier
// ---------------------------------------------------------------------------

func TestWorkspacePath_Deterministic(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(root, HooksConfig{})

	id := "PROJ-42"
	p1 := mgr.WorkspacePath(id)
	p2 := mgr.WorkspacePath(id)
	if p1 != p2 {
		t.Fatalf("workspace path not deterministic: %q vs %q", p1, p2)
	}
	// Path should be root / sanitized identifier
	want := filepath.Join(root, SanitizeIdentifier(id))
	if p1 != want {
		t.Fatalf("workspace path = %q, want %q", p1, want)
	}
}

// ---------------------------------------------------------------------------
// ValidateWorkspacePath
// ---------------------------------------------------------------------------

func TestValidateWorkspacePath_UnderRoot(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(root, HooksConfig{})

	validPath := filepath.Join(root, "some-issue")
	if err := mgr.ValidateWorkspacePath(validPath); err != nil {
		t.Fatalf("expected valid workspace path to pass, got error: %v", err)
	}
}

func TestValidateWorkspacePath_OutsideRoot_Rejected(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(root, HooksConfig{})

	outsidePath := filepath.Join(root, "..", "escape")
	err := mgr.ValidateWorkspacePath(outsidePath)
	if err == nil {
		t.Fatal("expected error for workspace path outside root, got nil")
	}
	if !strings.Contains(err.Error(), "not under root") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestValidateWorkspacePath_RootItself_Rejected(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(root, HooksConfig{})

	// The root itself should not pass because it is not strictly under root.
	err := mgr.ValidateWorkspacePath(root)
	if err == nil {
		t.Fatal("expected error when workspace path equals root, got nil")
	}
}

func TestValidateWorkspacePath_PrefixCollision_Rejected(t *testing.T) {
	// Ensure "/workspace-foo" does not match root "/workspace".
	parent := t.TempDir()
	root := filepath.Join(parent, "workspace")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(root, HooksConfig{})

	trickPath := filepath.Join(parent, "workspace-foo")
	err := mgr.ValidateWorkspacePath(trickPath)
	if err == nil {
		t.Fatal("expected error for prefix-collision path, got nil")
	}
}

// ---------------------------------------------------------------------------
// CreateForIssue — missing workspace directory is created
// ---------------------------------------------------------------------------

func TestCreateForIssue_CreatesNewDirectory(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(root, HooksConfig{})

	ws, err := mgr.CreateForIssue(context.Background(), "PROJ-100")
	if err != nil {
		t.Fatalf("CreateForIssue failed: %v", err)
	}
	if !ws.CreatedNow {
		t.Error("expected CreatedNow=true for a new workspace")
	}
	info, statErr := os.Stat(ws.Path)
	if statErr != nil {
		t.Fatalf("workspace directory not found: %v", statErr)
	}
	if !info.IsDir() {
		t.Fatal("workspace path is not a directory")
	}
	if ws.WorkspaceKey != SanitizeIdentifier("PROJ-100") {
		t.Errorf("WorkspaceKey = %q, want %q", ws.WorkspaceKey, SanitizeIdentifier("PROJ-100"))
	}
}

// ---------------------------------------------------------------------------
// CreateForIssue — existing workspace directory is reused
// ---------------------------------------------------------------------------

func TestCreateForIssue_ReusesExistingDirectory(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(root, HooksConfig{})

	// Pre-create the directory.
	sanitized := SanitizeIdentifier("PROJ-200")
	wsDir := filepath.Join(root, sanitized)
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	ws, err := mgr.CreateForIssue(context.Background(), "PROJ-200")
	if err != nil {
		t.Fatalf("CreateForIssue failed: %v", err)
	}
	if ws.CreatedNow {
		t.Error("expected CreatedNow=false for existing workspace")
	}
	if ws.Path != wsDir {
		t.Errorf("Path = %q, want %q", ws.Path, wsDir)
	}
}

// ---------------------------------------------------------------------------
// CreateForIssue — existing non-directory path returns error
// ---------------------------------------------------------------------------

func TestCreateForIssue_NonDirectoryPath_ReturnsError(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(root, HooksConfig{})

	// Place a regular file where the workspace directory would be.
	sanitized := SanitizeIdentifier("PROJ-300")
	filePath := filepath.Join(root, sanitized)
	if err := os.WriteFile(filePath, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := mgr.CreateForIssue(context.Background(), "PROJ-300")
	if err == nil {
		t.Fatal("expected error when workspace path is a file, got nil")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

// ---------------------------------------------------------------------------
// after_create hook runs only on new workspace creation
// ---------------------------------------------------------------------------

func TestCreateForIssue_AfterCreateHook_RunsOnNew(t *testing.T) {
	root := t.TempDir()
	markerFile := filepath.Join(root, "hook_ran")

	mgr := NewManager(root, HooksConfig{
		AfterCreate: "touch " + markerFile,
		TimeoutMs:   5000,
	})

	ws, err := mgr.CreateForIssue(context.Background(), "HOOK-NEW")
	if err != nil {
		t.Fatalf("CreateForIssue failed: %v", err)
	}
	if !ws.CreatedNow {
		t.Fatal("expected CreatedNow=true")
	}
	if _, statErr := os.Stat(markerFile); statErr != nil {
		t.Fatal("after_create hook did not run: marker file missing")
	}
}

func TestCreateForIssue_AfterCreateHook_SkippedOnExisting(t *testing.T) {
	root := t.TempDir()
	markerFile := filepath.Join(root, "hook_should_not_run")

	mgr := NewManager(root, HooksConfig{
		AfterCreate: "touch " + markerFile,
		TimeoutMs:   5000,
	})

	// Pre-create the workspace directory.
	sanitized := SanitizeIdentifier("HOOK-EXIST")
	if err := os.MkdirAll(filepath.Join(root, sanitized), 0o755); err != nil {
		t.Fatal(err)
	}

	ws, err := mgr.CreateForIssue(context.Background(), "HOOK-EXIST")
	if err != nil {
		t.Fatalf("CreateForIssue failed: %v", err)
	}
	if ws.CreatedNow {
		t.Fatal("expected CreatedNow=false")
	}
	if _, statErr := os.Stat(markerFile); statErr == nil {
		t.Fatal("after_create hook should not have run for existing workspace")
	}
}

func TestCreateForIssue_AfterCreateHook_FailureCleansUp(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(root, HooksConfig{
		AfterCreate: "exit 1",
		TimeoutMs:   5000,
	})

	_, err := mgr.CreateForIssue(context.Background(), "HOOK-FAIL")
	if err == nil {
		t.Fatal("expected error when after_create hook fails, got nil")
	}
	if !strings.Contains(err.Error(), "after_create hook failed") {
		t.Fatalf("unexpected error message: %v", err)
	}
	// Workspace directory should have been removed.
	wsPath := filepath.Join(root, SanitizeIdentifier("HOOK-FAIL"))
	if _, statErr := os.Stat(wsPath); !os.IsNotExist(statErr) {
		t.Fatal("workspace directory should have been removed after hook failure")
	}
}

// ---------------------------------------------------------------------------
// before_run hook failure aborts the current attempt
// ---------------------------------------------------------------------------

func TestRunBeforeRun_Success(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(root, HooksConfig{
		BeforeRun: "true",
		TimeoutMs: 5000,
	})

	wsDir := filepath.Join(root, "ws")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := mgr.RunBeforeRun(context.Background(), wsDir); err != nil {
		t.Fatalf("RunBeforeRun should succeed: %v", err)
	}
}

func TestRunBeforeRun_Failure_Aborts(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(root, HooksConfig{
		BeforeRun: "exit 1",
		TimeoutMs: 5000,
	})

	wsDir := filepath.Join(root, "ws")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	err := mgr.RunBeforeRun(context.Background(), wsDir)
	if err == nil {
		t.Fatal("expected error when before_run hook fails, got nil")
	}
	if !strings.Contains(err.Error(), "before_run hook failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunBeforeRun_NoHook_Noop(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(root, HooksConfig{})

	wsDir := filepath.Join(root, "ws")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := mgr.RunBeforeRun(context.Background(), wsDir); err != nil {
		t.Fatalf("RunBeforeRun with no hook should succeed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// after_run hook failure is logged and ignored
// ---------------------------------------------------------------------------

func TestRunAfterRun_Success(t *testing.T) {
	root := t.TempDir()
	markerFile := filepath.Join(root, "after_run_marker")
	mgr := NewManager(root, HooksConfig{
		AfterRun:  "touch " + markerFile,
		TimeoutMs: 5000,
	})

	wsDir := filepath.Join(root, "ws")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// RunAfterRun returns nothing — it should not panic.
	mgr.RunAfterRun(context.Background(), wsDir)

	if _, err := os.Stat(markerFile); err != nil {
		t.Fatal("after_run hook did not run: marker file missing")
	}
}

func TestRunAfterRun_Failure_Ignored(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(root, HooksConfig{
		AfterRun:  "exit 1",
		TimeoutMs: 5000,
	})

	wsDir := filepath.Join(root, "ws")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Should not panic — errors are logged and ignored.
	mgr.RunAfterRun(context.Background(), wsDir)
}

func TestRunAfterRun_NoHook_Noop(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(root, HooksConfig{})

	wsDir := filepath.Join(root, "ws")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	mgr.RunAfterRun(context.Background(), wsDir)
}

// ---------------------------------------------------------------------------
// CleanWorkspace — before_remove hook runs on cleanup
// ---------------------------------------------------------------------------

func TestCleanWorkspace_RunsBeforeRemoveHook(t *testing.T) {
	root := t.TempDir()
	markerFile := filepath.Join(root, "before_remove_ran")

	mgr := NewManager(root, HooksConfig{
		BeforeRemove: "touch " + markerFile,
		TimeoutMs:    5000,
	})

	// Create the workspace directory so CleanWorkspace has something to clean.
	identifier := "CLEAN-ME"
	sanitized := SanitizeIdentifier(identifier)
	wsDir := filepath.Join(root, sanitized)
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	mgr.CleanWorkspace(context.Background(), identifier)

	// Marker file should exist (hook ran).
	if _, err := os.Stat(markerFile); err != nil {
		t.Fatal("before_remove hook did not run: marker file missing")
	}
	// Workspace directory should have been removed.
	if _, err := os.Stat(wsDir); !os.IsNotExist(err) {
		t.Fatal("workspace directory should have been removed after CleanWorkspace")
	}
}

func TestCleanWorkspace_NonExistentWorkspace_Noop(t *testing.T) {
	root := t.TempDir()
	markerFile := filepath.Join(root, "hook_should_not_run")

	mgr := NewManager(root, HooksConfig{
		BeforeRemove: "touch " + markerFile,
		TimeoutMs:    5000,
	})

	// Call CleanWorkspace for a non-existent workspace — should be a no-op.
	mgr.CleanWorkspace(context.Background(), "DOES-NOT-EXIST")

	if _, err := os.Stat(markerFile); err == nil {
		t.Fatal("before_remove hook should not have run for non-existent workspace")
	}
}

func TestCleanWorkspace_BeforeRemoveHookFailure_StillRemoves(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(root, HooksConfig{
		BeforeRemove: "exit 1",
		TimeoutMs:    5000,
	})

	identifier := "CLEAN-FAIL"
	sanitized := SanitizeIdentifier(identifier)
	wsDir := filepath.Join(root, sanitized)
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Hook failure is logged and ignored; directory should still be removed.
	mgr.CleanWorkspace(context.Background(), identifier)

	if _, err := os.Stat(wsDir); !os.IsNotExist(err) {
		t.Fatal("workspace directory should be removed even when before_remove hook fails")
	}
}

// ---------------------------------------------------------------------------
// Hook timeout is applied
// ---------------------------------------------------------------------------

func TestHookTimeout_Applied(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(root, HooksConfig{
		// Use a very short timeout so the hook is killed quickly.
		BeforeRun: "sleep 60",
		TimeoutMs: 200,
	})

	wsDir := filepath.Join(root, "ws")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	err := mgr.RunBeforeRun(context.Background(), wsDir)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error from timed-out hook, got nil")
	}
	// The hook should have been killed well before 60 seconds.
	if elapsed > 5*time.Second {
		t.Fatalf("hook took too long (%v); timeout was not applied", elapsed)
	}
}

func TestHookTimeout_DefaultApplied(t *testing.T) {
	mgr := NewManager("/tmp", HooksConfig{TimeoutMs: 0})
	timeout := mgr.hookTimeout()
	want := time.Duration(defaultHookTimeoutMs) * time.Millisecond
	if timeout != want {
		t.Errorf("default hookTimeout = %v, want %v", timeout, want)
	}
}

func TestHookTimeout_NegativeValue_DefaultApplied(t *testing.T) {
	mgr := NewManager("/tmp", HooksConfig{TimeoutMs: -100})
	timeout := mgr.hookTimeout()
	want := time.Duration(defaultHookTimeoutMs) * time.Millisecond
	if timeout != want {
		t.Errorf("hookTimeout with negative value = %v, want %v", timeout, want)
	}
}

func TestHookTimeout_CustomValue(t *testing.T) {
	mgr := NewManager("/tmp", HooksConfig{TimeoutMs: 3000})
	timeout := mgr.hookTimeout()
	want := 3000 * time.Millisecond
	if timeout != want {
		t.Errorf("hookTimeout = %v, want %v", timeout, want)
	}
}

// ---------------------------------------------------------------------------
// Hook working directory is the workspace path
// ---------------------------------------------------------------------------

func TestHook_RunsInWorkspaceDirectory(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(root, HooksConfig{
		// pwd writes the current directory; verify it matches the workspace dir.
		BeforeRun: "pwd > pwd_output.txt",
		TimeoutMs: 5000,
	})

	identifier := "DIR-CHECK"
	ws, err := mgr.CreateForIssue(context.Background(), identifier)
	if err != nil {
		t.Fatalf("CreateForIssue failed: %v", err)
	}

	if err := mgr.RunBeforeRun(context.Background(), ws.Path); err != nil {
		t.Fatalf("RunBeforeRun failed: %v", err)
	}

	outputFile := filepath.Join(ws.Path, "pwd_output.txt")
	data, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("failed to read pwd output: %v", err)
	}
	got := strings.TrimSpace(string(data))
	// Resolve symlinks for comparison (macOS /tmp is a symlink).
	wantResolved, _ := filepath.EvalSymlinks(ws.Path)
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != wantResolved {
		t.Errorf("hook ran in %q, want %q", gotResolved, wantResolved)
	}
}

// ---------------------------------------------------------------------------
// UpdateConfig
// ---------------------------------------------------------------------------

func TestUpdateConfig(t *testing.T) {
	mgr := NewManager("/old-root", HooksConfig{})

	newRoot := t.TempDir()
	newHooks := HooksConfig{
		AfterCreate:  "echo created",
		BeforeRun:    "echo before",
		AfterRun:     "echo after",
		BeforeRemove: "echo remove",
		TimeoutMs:    9999,
	}
	mgr.UpdateConfig(newRoot, newHooks)

	if mgr.root != newRoot {
		t.Errorf("root = %q, want %q", mgr.root, newRoot)
	}
	if mgr.hooks != newHooks {
		t.Errorf("hooks not updated correctly")
	}
}

// ---------------------------------------------------------------------------
// Context cancellation propagates to hook
// ---------------------------------------------------------------------------

func TestHook_ContextCancellation(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(root, HooksConfig{
		BeforeRun: "sleep 60",
		TimeoutMs: 60000,
	})

	wsDir := filepath.Join(root, "ws")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after a short delay.
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	err := mgr.RunBeforeRun(ctx, wsDir)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error from cancelled hook, got nil")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("hook took too long (%v); context cancellation was not propagated", elapsed)
	}
}

// ---------------------------------------------------------------------------
// Workspace path sanitization with special identifiers
// ---------------------------------------------------------------------------

func TestCreateForIssue_SanitizesIdentifier(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(root, HooksConfig{})

	ws, err := mgr.CreateForIssue(context.Background(), "org/PROJ#42")
	if err != nil {
		t.Fatalf("CreateForIssue failed: %v", err)
	}

	// The path should contain only safe characters.
	base := filepath.Base(ws.Path)
	if base != "org_PROJ_42" {
		t.Errorf("workspace basename = %q, want %q", base, "org_PROJ_42")
	}
}
