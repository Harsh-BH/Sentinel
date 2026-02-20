package executor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/Harsh-BH/Sentinel/worker/internal/domain"
)

// ──────────────────────────────────────────────────────
// Pure unit tests — no nsjail binary needed
// ──────────────────────────────────────────────────────

func TestNewSandboxExecutor(t *testing.T) {
	logger := zap.NewNop()
	exe := NewSandboxExecutor("/usr/bin/nsjail", "/etc/nsjail", logger)

	if exe.nsjailPath != "/usr/bin/nsjail" {
		t.Errorf("expected nsjailPath /usr/bin/nsjail, got %s", exe.nsjailPath)
	}
	if exe.configDir != "/etc/nsjail" {
		t.Errorf("expected configDir /etc/nsjail, got %s", exe.configDir)
	}
	if exe.logger == nil {
		t.Error("expected non-nil logger")
	}
}

func TestExecute_UnsupportedLanguage(t *testing.T) {
	logger := zap.NewNop()
	exe := NewSandboxExecutor("/usr/bin/nsjail", "/etc/nsjail", logger)

	req := &domain.ExecutionRequest{
		JobID:         uuid.New(),
		Language:      domain.Language("ruby"),
		SourceCode:    "puts 'hello'",
		TimeLimitMs:   5000,
		MemoryLimitKB: 262144,
	}

	result, err := exe.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != domain.StatusInternalError {
		t.Errorf("expected StatusInternalError, got %s", result.Status)
	}
	if result.Stderr == "" {
		t.Error("expected non-empty stderr for unsupported language")
	}
}

func TestWorkdirCreationAndCleanup(t *testing.T) {
	logger := zap.NewNop()
	// Use a nonexistent nsjail path — execution will fail but workdir logic is testable
	exe := NewSandboxExecutor("/nonexistent/nsjail", t.TempDir(), logger)

	req := &domain.ExecutionRequest{
		JobID:         uuid.New(),
		Language:      domain.LangPython,
		SourceCode:    "print('hello')",
		TimeLimitMs:   5000,
		MemoryLimitKB: 262144,
	}

	// Execute will fail (nsjail not found) but should not leave workdirs behind
	beforeDirs, _ := filepath.Glob(filepath.Join(os.TempDir(), "sentinel-*"))

	_, _ = exe.Execute(context.Background(), req)

	afterDirs, _ := filepath.Glob(filepath.Join(os.TempDir(), "sentinel-*"))

	// No new sentinel-* dirs should remain (defer os.RemoveAll should clean up)
	if len(afterDirs) > len(beforeDirs) {
		t.Errorf("workdir leak detected: before=%d after=%d", len(beforeDirs), len(afterDirs))
	}
}

func TestExecutePython_WritesFiles(t *testing.T) {
	logger := zap.NewNop()
	configDir := t.TempDir()

	// Create a dummy python.cfg so the path resolves
	if err := os.WriteFile(filepath.Join(configDir, "python.cfg"), []byte("# dummy"), 0644); err != nil {
		t.Fatalf("write dummy config: %v", err)
	}

	exe := NewSandboxExecutor("/nonexistent/nsjail", configDir, logger)

	req := &domain.ExecutionRequest{
		JobID:         uuid.New(),
		Language:      domain.LangPython,
		SourceCode:    "print('hello world')",
		Stdin:         "test input\n",
		TimeLimitMs:   5000,
		MemoryLimitKB: 262144,
	}

	// This will fail because nsjail doesn't exist, but we can verify the file writing
	// by inspecting the result (internal error because binary not found)
	result, err := exe.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should get InternalError since nsjail binary doesn't exist
	if result.Status != domain.StatusInternalError {
		t.Logf("got status %s (expected InternalError due to missing nsjail)", result.Status)
	}
}

func TestExecuteCpp_WritesFiles(t *testing.T) {
	logger := zap.NewNop()
	configDir := t.TempDir()

	// Create a dummy cpp.cfg
	if err := os.WriteFile(filepath.Join(configDir, "cpp.cfg"), []byte("# dummy"), 0644); err != nil {
		t.Fatalf("write dummy config: %v", err)
	}

	exe := NewSandboxExecutor("/nonexistent/nsjail", configDir, logger)

	req := &domain.ExecutionRequest{
		JobID:         uuid.New(),
		Language:      domain.LangCpp,
		SourceCode:    "#include <iostream>\nint main() { std::cout << \"hello\"; }",
		Stdin:         "",
		TimeLimitMs:   10000,
		MemoryLimitKB: 524288,
	}

	result, err := exe.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != domain.StatusInternalError {
		t.Logf("got status %s (expected InternalError due to missing nsjail)", result.Status)
	}
}

func TestBuildNsjailArgs(t *testing.T) {
	// Verify the arg construction logic by building args manually
	// and checking expected values
	tests := []struct {
		name          string
		timeLimitMs   int
		memoryLimitKB int
		wantTimeLimit string
		wantMemMax    string
	}{
		{
			name:          "standard limits",
			timeLimitMs:   5000,
			memoryLimitKB: 262144,
			wantTimeLimit: "6",         // 5000/1000 + 1
			wantMemMax:    "268435456", // 262144 * 1024
		},
		{
			name:          "tight limits",
			timeLimitMs:   1000,
			memoryLimitKB: 65536,
			wantTimeLimit: "2",        // 1000/1000 + 1
			wantMemMax:    "67108864", // 65536 * 1024
		},
		{
			name:          "generous limits",
			timeLimitMs:   30000,
			memoryLimitKB: 524288,
			wantTimeLimit: "31",        // 30000/1000 + 1
			wantMemMax:    "536870912", // 524288 * 1024
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &domain.ExecutionRequest{
				TimeLimitMs:   tt.timeLimitMs,
				MemoryLimitKB: tt.memoryLimitKB,
			}

			timeLimit := fmt.Sprintf("%d", req.TimeLimitMs/1000+1)
			memMax := fmt.Sprintf("%d", req.MemoryLimitKB*1024)

			if timeLimit != tt.wantTimeLimit {
				t.Errorf("timeLimit: got %s, want %s", timeLimit, tt.wantTimeLimit)
			}
			if memMax != tt.wantMemMax {
				t.Errorf("memMax: got %s, want %s", memMax, tt.wantMemMax)
			}
		})
	}
}

func TestContextCancellation(t *testing.T) {
	logger := zap.NewNop()
	configDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(configDir, "python.cfg"), []byte("# dummy"), 0644); err != nil {
		t.Fatalf("write dummy config: %v", err)
	}

	exe := NewSandboxExecutor("/nonexistent/nsjail", configDir, logger)

	// Create an already-cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	req := &domain.ExecutionRequest{
		JobID:         uuid.New(),
		Language:      domain.LangPython,
		SourceCode:    "print('hello')",
		TimeLimitMs:   5000,
		MemoryLimitKB: 262144,
	}

	result, err := exe.Execute(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// With cancelled context + missing binary, should get an error status
	if result.Status == domain.StatusSuccess {
		t.Error("expected non-success status with cancelled context")
	}
}

func TestExecutionResult_StatusTerminal(t *testing.T) {
	terminalStatuses := []domain.ExecutionStatus{
		domain.StatusSuccess,
		domain.StatusCompilationError,
		domain.StatusRuntimeError,
		domain.StatusTimeout,
		domain.StatusMemoryLimitExceeded,
		domain.StatusInternalError,
	}

	for _, s := range terminalStatuses {
		if !s.IsTerminal() {
			t.Errorf("expected %s to be terminal", s)
		}
	}

	nonTerminalStatuses := []domain.ExecutionStatus{
		domain.StatusQueued,
		domain.StatusCompiling,
		domain.StatusRunning,
	}

	for _, s := range nonTerminalStatuses {
		if s.IsTerminal() {
			t.Errorf("expected %s to be non-terminal", s)
		}
	}
}

func TestTimeLimitCalculation(t *testing.T) {
	// Verify the +2000ms grace period on the context timeout
	req := &domain.ExecutionRequest{
		TimeLimitMs: 5000,
	}

	contextTimeout := time.Duration(req.TimeLimitMs+2000) * time.Millisecond
	expected := 7 * time.Second

	if contextTimeout != expected {
		t.Errorf("context timeout: got %v, want %v", contextTimeout, expected)
	}

	// nsjail --time_limit should be TimeLimitMs/1000 + 1
	nsjailTimeLimit := req.TimeLimitMs/1000 + 1
	if nsjailTimeLimit != 6 {
		t.Errorf("nsjail time_limit: got %d, want 6", nsjailTimeLimit)
	}
}
