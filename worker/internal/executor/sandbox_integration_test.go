//go:build integration

package executor

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/Harsh-BH/Sentinel/worker/internal/domain"
)

// ──────────────────────────────────────────────────────
// Integration tests — require nsjail installed
// Run with: go test -tags integration -v ./internal/executor/
// ──────────────────────────────────────────────────────

func skipIfNoNsjail(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("nsjail"); err != nil {
		t.Skip("nsjail not found in PATH — skipping integration test")
	}
}

func skipIfNotRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("must run as root for nsjail namespace creation — skipping integration test")
	}
}

func newIntegrationExecutor(t *testing.T) *SandboxExecutor {
	t.Helper()
	skipIfNoNsjail(t)
	skipIfNotRoot(t)

	logger, _ := zap.NewDevelopment()

	nsjailPath, _ := exec.LookPath("nsjail")
	configDir := os.Getenv("SENTINEL_NSJAIL_CONFIG_DIR")
	if configDir == "" {
		configDir = "/etc/nsjail"
	}

	return NewSandboxExecutor(nsjailPath, configDir, logger)
}

func TestIntegration_PythonHelloWorld(t *testing.T) {
	exe := newIntegrationExecutor(t)

	req := &domain.ExecutionRequest{
		JobID:         uuid.New(),
		Language:      domain.LangPython,
		SourceCode:    "print('Hello, Sentinel!')",
		Stdin:         "",
		TimeLimitMs:   5000,
		MemoryLimitKB: 262144,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	result, err := exe.Execute(ctx, req)
	if err != nil {
		t.Fatalf("execution error: %v", err)
	}

	if result.Status != domain.StatusSuccess {
		t.Errorf("expected SUCCESS, got %s (stderr: %s)", result.Status, result.Stderr)
	}
	if result.Stdout != "Hello, Sentinel!\n" {
		t.Errorf("expected 'Hello, Sentinel!\\n', got %q", result.Stdout)
	}
}

func TestIntegration_PythonStdin(t *testing.T) {
	exe := newIntegrationExecutor(t)

	req := &domain.ExecutionRequest{
		JobID:         uuid.New(),
		Language:      domain.LangPython,
		SourceCode:    "name = input()\nprint(f'Hello, {name}!')",
		Stdin:         "World\n",
		TimeLimitMs:   5000,
		MemoryLimitKB: 262144,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	result, err := exe.Execute(ctx, req)
	if err != nil {
		t.Fatalf("execution error: %v", err)
	}

	if result.Status != domain.StatusSuccess {
		t.Errorf("expected SUCCESS, got %s (stderr: %s)", result.Status, result.Stderr)
	}
	if result.Stdout != "Hello, World!\n" {
		t.Errorf("expected 'Hello, World!\\n', got %q", result.Stdout)
	}
}

func TestIntegration_PythonTimeout(t *testing.T) {
	exe := newIntegrationExecutor(t)

	req := &domain.ExecutionRequest{
		JobID:         uuid.New(),
		Language:      domain.LangPython,
		SourceCode:    "import time\nwhile True: time.sleep(1)",
		Stdin:         "",
		TimeLimitMs:   2000,
		MemoryLimitKB: 262144,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	result, err := exe.Execute(ctx, req)
	if err != nil {
		t.Fatalf("execution error: %v", err)
	}

	if result.Status != domain.StatusTimeout {
		t.Errorf("expected TIMEOUT, got %s", result.Status)
	}
}

func TestIntegration_PythonMemoryLimit(t *testing.T) {
	exe := newIntegrationExecutor(t)

	req := &domain.ExecutionRequest{
		JobID:         uuid.New(),
		Language:      domain.LangPython,
		SourceCode:    "x = [0] * (1024 * 1024 * 500)",
		Stdin:         "",
		TimeLimitMs:   5000,
		MemoryLimitKB: 65536, // 64 MB — too small for 500M list
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	result, err := exe.Execute(ctx, req)
	if err != nil {
		t.Fatalf("execution error: %v", err)
	}

	if result.Status != domain.StatusMemoryLimitExceeded && result.Status != domain.StatusRuntimeError {
		t.Errorf("expected MEMORY_LIMIT_EXCEEDED or RUNTIME_ERROR, got %s", result.Status)
	}
}

func TestIntegration_CppHelloWorld(t *testing.T) {
	exe := newIntegrationExecutor(t)

	req := &domain.ExecutionRequest{
		JobID:    uuid.New(),
		Language: domain.LangCpp,
		SourceCode: `#include <iostream>
int main() {
    std::cout << "Hello, Sentinel!" << std::endl;
    return 0;
}`,
		Stdin:         "",
		TimeLimitMs:   10000,
		MemoryLimitKB: 524288,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := exe.Execute(ctx, req)
	if err != nil {
		t.Fatalf("execution error: %v", err)
	}

	if result.Status != domain.StatusSuccess {
		t.Errorf("expected SUCCESS, got %s (stderr: %s)", result.Status, result.Stderr)
	}
	if result.Stdout != "Hello, Sentinel!\n" {
		t.Errorf("expected 'Hello, Sentinel!\\n', got %q", result.Stdout)
	}
}

func TestIntegration_CppCompilationError(t *testing.T) {
	exe := newIntegrationExecutor(t)

	req := &domain.ExecutionRequest{
		JobID:         uuid.New(),
		Language:      domain.LangCpp,
		SourceCode:    "this is not valid cpp",
		Stdin:         "",
		TimeLimitMs:   10000,
		MemoryLimitKB: 524288,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := exe.Execute(ctx, req)
	if err != nil {
		t.Fatalf("execution error: %v", err)
	}

	if result.Status != domain.StatusCompilationError {
		t.Errorf("expected COMPILATION_ERROR, got %s", result.Status)
	}
	if result.Stderr == "" {
		t.Error("expected compiler error output in stderr")
	}
}

func TestIntegration_CppSegfault(t *testing.T) {
	exe := newIntegrationExecutor(t)

	req := &domain.ExecutionRequest{
		JobID:    uuid.New(),
		Language: domain.LangCpp,
		SourceCode: `#include <cstdlib>
int main() {
    int *p = nullptr;
    *p = 42;
    return 0;
}`,
		Stdin:         "",
		TimeLimitMs:   10000,
		MemoryLimitKB: 524288,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := exe.Execute(ctx, req)
	if err != nil {
		t.Fatalf("execution error: %v", err)
	}

	if result.Status != domain.StatusRuntimeError {
		t.Errorf("expected RUNTIME_ERROR, got %s", result.Status)
	}
}

func TestIntegration_PythonNetworkBlocked(t *testing.T) {
	exe := newIntegrationExecutor(t)

	req := &domain.ExecutionRequest{
		JobID:    uuid.New(),
		Language: domain.LangPython,
		SourceCode: `import socket
s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
s.connect(("8.8.8.8", 53))
print("SHOULD NOT REACH HERE")`,
		Stdin:         "",
		TimeLimitMs:   5000,
		MemoryLimitKB: 262144,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	result, err := exe.Execute(ctx, req)
	if err != nil {
		t.Fatalf("execution error: %v", err)
	}

	if result.Status == domain.StatusSuccess {
		t.Error("expected network access to be blocked, but execution succeeded")
	}
	if result.Stdout == "SHOULD NOT REACH HERE\n" {
		t.Error("process was able to reach the network — sandbox network isolation broken")
	}
}

func TestIntegration_PythonForkBomb(t *testing.T) {
	exe := newIntegrationExecutor(t)

	req := &domain.ExecutionRequest{
		JobID:    uuid.New(),
		Language: domain.LangPython,
		SourceCode: `import os
while True:
    os.fork()`,
		Stdin:         "",
		TimeLimitMs:   5000,
		MemoryLimitKB: 262144,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	result, err := exe.Execute(ctx, req)
	if err != nil {
		t.Fatalf("execution error: %v", err)
	}

	// Should be contained — either runtime error (pids limit) or timeout
	if result.Status == domain.StatusSuccess {
		t.Error("fork bomb should not succeed — cgroup pids limit should prevent it")
	}
}
