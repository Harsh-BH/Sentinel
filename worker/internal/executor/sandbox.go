package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/Harsh-BH/Sentinel/worker/internal/domain"
)

const (
	// maxOutputBytes caps stdout/stderr to prevent memory exhaustion.
	maxOutputBytes = 64 * 1024 // 64 KB

	// outputTruncatedMsg is appended when output exceeds the limit.
	outputTruncatedMsg = "\n... output truncated (64 KB limit) ..."
)

// SandboxExecutor runs code inside an nsjail sandbox.
type SandboxExecutor struct {
	nsjailPath string
	configDir  string
	logger     *zap.Logger
}

// NewSandboxExecutor creates a new sandbox executor.
func NewSandboxExecutor(nsjailPath, configDir string, logger *zap.Logger) *SandboxExecutor {
	return &SandboxExecutor{
		nsjailPath: nsjailPath,
		configDir:  configDir,
		logger:     logger,
	}
}

// Execute runs the given code in an nsjail sandbox and returns the result.
func (e *SandboxExecutor) Execute(ctx context.Context, req *domain.ExecutionRequest) (*domain.ExecutionResult, error) {
	// Create an ephemeral working directory
	workDir, err := os.MkdirTemp("", fmt.Sprintf("sentinel-%s-*", req.JobID.String()))
	if err != nil {
		return nil, fmt.Errorf("create work dir: %w", err)
	}
	defer os.RemoveAll(workDir)

	switch req.Language {
	case domain.LangPython:
		return e.executePython(ctx, req, workDir)
	case domain.LangCpp:
		return e.executeCpp(ctx, req, workDir)
	default:
		return &domain.ExecutionResult{
			Status: domain.StatusInternalError,
			Stderr: "unsupported language: " + string(req.Language),
		}, nil
	}
}

func (e *SandboxExecutor) executePython(ctx context.Context, req *domain.ExecutionRequest, workDir string) (*domain.ExecutionResult, error) {
	// Write source code to file
	codePath := filepath.Join(workDir, "code.py")
	if err := os.WriteFile(codePath, []byte(req.SourceCode), 0644); err != nil {
		return nil, fmt.Errorf("write source: %w", err)
	}

	// Write stdin to file
	stdinPath := filepath.Join(workDir, "stdin.txt")
	if err := os.WriteFile(stdinPath, []byte(req.Stdin), 0644); err != nil {
		return nil, fmt.Errorf("write stdin: %w", err)
	}

	configPath := filepath.Join(e.configDir, "python.cfg")
	return e.runNsjail(ctx, req, configPath, workDir, "/usr/bin/python3", "/tmp/work/code.py")
}

func (e *SandboxExecutor) executeCpp(ctx context.Context, req *domain.ExecutionRequest, workDir string) (*domain.ExecutionResult, error) {
	// Write source code to file
	codePath := filepath.Join(workDir, "code.cpp")
	if err := os.WriteFile(codePath, []byte(req.SourceCode), 0644); err != nil {
		return nil, fmt.Errorf("write source: %w", err)
	}

	// Write stdin to file
	stdinPath := filepath.Join(workDir, "stdin.txt")
	if err := os.WriteFile(stdinPath, []byte(req.Stdin), 0644); err != nil {
		return nil, fmt.Errorf("write stdin: %w", err)
	}

	configPath := filepath.Join(e.configDir, "cpp.cfg")

	// Phase 1: Compile
	compileCtx, compileCancel := context.WithTimeout(ctx, 10*time.Second)
	defer compileCancel()

	compileResult, err := e.runNsjail(compileCtx, req, configPath, workDir,
		"/usr/bin/g++", "-std=c++17", "-O2", "-o", "/tmp/work/program", "/tmp/work/code.cpp")
	if err != nil {
		return nil, fmt.Errorf("compile: %w", err)
	}
	if compileResult.ExitCode != 0 {
		compileResult.Status = domain.StatusCompilationError
		return compileResult, nil
	}

	// Phase 2: Execute
	return e.runNsjail(ctx, req, configPath, workDir, "/tmp/work/program")
}

func (e *SandboxExecutor) runNsjail(
	ctx context.Context,
	req *domain.ExecutionRequest,
	configPath, workDir string,
	execArgs ...string,
) (*domain.ExecutionResult, error) {
	// Build nsjail command
	args := []string{
		"--config", configPath,
		"--bindmount", workDir + ":/tmp/work",
		"--time_limit", fmt.Sprintf("%d", req.TimeLimitMs/1000+1),
		"--cgroup_mem_max", fmt.Sprintf("%d", req.MemoryLimitKB*1024),
		"--",
	}
	args = append(args, execArgs...)

	timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(req.TimeLimitMs+2000)*time.Millisecond)
	defer cancel()

	cmd := exec.CommandContext(timeoutCtx, e.nsjailPath, args...)

	// Set up process group for clean termination
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Set up stdin from file
	stdinFile := filepath.Join(workDir, "stdin.txt")
	if stdinData, err := os.ReadFile(stdinFile); err == nil {
		cmd.Stdin = bytes.NewReader(stdinData)
	}

	// Use limited writers to cap output size and prevent OOM on host
	var stdout, stderr limitedBuffer
	stdout.limit = maxOutputBytes
	stderr.limit = maxOutputBytes
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	startTime := time.Now()
	err := cmd.Run()
	elapsed := time.Since(startTime)

	// Separate nsjail log lines from actual program stderr.
	// nsjail prefixes its log lines with "[I]", "[W]", "[E]", "[F]", "[D]".
	progStderr, nsjailLog := separateNsjailLogs(stderr.String())

	result := &domain.ExecutionResult{
		Stdout:     truncateOutput(stdout.String(), stdout.truncated),
		Stderr:     truncateOutput(progStderr, false),
		ExitCode:   0,
		TimeUsedMs: int(elapsed.Milliseconds()),
	}

	// Try to read memory usage from cgroup (if available)
	result.MemoryUsedKB = readCgroupMemoryPeak(workDir)

	e.logger.Debug("nsjail execution completed",
		zap.String("job_id", req.JobID.String()),
		zap.Duration("elapsed", elapsed),
		zap.Int("exit_code", result.ExitCode),
		zap.Int("memory_used_kb", result.MemoryUsedKB),
		zap.String("nsjail_log", nsjailLog),
	)

	if timeoutCtx.Err() == context.DeadlineExceeded {
		// Kill entire process group
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		result.Status = domain.StatusTimeout
		result.ExitCode = -1
		return result, nil
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
			// Check if killed by OOM (exit code 137 = SIGKILL from cgroup OOM killer)
			if isOOMKill(exitErr.ExitCode(), nsjailLog) {
				result.Status = domain.StatusMemoryLimitExceeded
			} else {
				result.Status = domain.StatusRuntimeError
			}
		} else {
			result.Status = domain.StatusInternalError
			result.Stderr = err.Error()
		}
		return result, nil
	}

	result.ExitCode = 0
	result.Status = domain.StatusSuccess
	return result, nil
}

// ──────────────────────────────────────────────────────
// Helper types and functions
// ──────────────────────────────────────────────────────

// limitedBuffer is a bytes.Buffer that stops accepting writes after a limit.
type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (lb *limitedBuffer) Write(p []byte) (n int, err error) {
	if lb.truncated {
		return len(p), nil // discard silently
	}

	remaining := lb.limit - lb.buf.Len()
	if remaining <= 0 {
		lb.truncated = true
		return len(p), nil
	}

	if len(p) > remaining {
		lb.truncated = true
		p = p[:remaining]
	}

	return lb.buf.Write(p)
}

func (lb *limitedBuffer) String() string {
	return lb.buf.String()
}

// truncateOutput appends a truncation notice if the output was cut off.
func truncateOutput(s string, wasTruncated bool) string {
	if wasTruncated {
		return s + outputTruncatedMsg
	}
	return s
}

// separateNsjailLogs splits nsjail log lines from the user program's stderr.
// nsjail logs are prefixed with bracketed tags like [I], [W], [E], [F], [D].
func separateNsjailLogs(rawStderr string) (programStderr, nsjailLogs string) {
	if rawStderr == "" {
		return "", ""
	}

	var progLines, logLines []string
	for _, line := range strings.Split(rawStderr, "\n") {
		trimmed := strings.TrimSpace(line)
		if isNsjailLogLine(trimmed) {
			logLines = append(logLines, line)
		} else {
			progLines = append(progLines, line)
		}
	}

	return strings.Join(progLines, "\n"), strings.Join(logLines, "\n")
}

// isNsjailLogLine returns true if the line looks like an nsjail log entry.
func isNsjailLogLine(line string) bool {
	nsjailPrefixes := []string{"[I]", "[W]", "[E]", "[F]", "[D]"}
	for _, prefix := range nsjailPrefixes {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

// isOOMKill checks if the process was killed due to an OOM condition.
// Exit code 137 = process received SIGKILL (128 + 9), which is the
// standard OOM kill signal from cgroups. We also check nsjail logs
// for memory-related messages.
func isOOMKill(exitCode int, nsjailLog string) bool {
	if exitCode == 137 {
		return true
	}
	// nsjail sometimes logs OOM events
	lowerLog := strings.ToLower(nsjailLog)
	return strings.Contains(lowerLog, "oom") ||
		strings.Contains(lowerLog, "memory cgroup") ||
		strings.Contains(lowerLog, "cgroup_mem")
}

// readCgroupMemoryPeak attempts to read the peak memory usage from cgroup v2.
// Returns 0 if unavailable (e.g., not running inside a cgroup or no permissions).
func readCgroupMemoryPeak(workDir string) int {
	// cgroup v2: /sys/fs/cgroup/<path>/memory.peak
	// We try common paths; in a container this may not be accessible.
	paths := []string{
		"/sys/fs/cgroup/memory.peak",
		"/sys/fs/cgroup/memory/memory.max_usage_in_bytes", // cgroup v1 fallback
	}

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		s := strings.TrimSpace(string(data))
		if s == "max" || s == "" {
			continue
		}
		val, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			continue
		}
		return int(val / 1024) // convert bytes to KB
	}

	return 0
}

// limitedReader wraps an io.Reader and caps reads at a byte limit.
// Used for piping stdin if needed in the future.
type limitedReader struct {
	r         io.Reader
	remaining int
}

func (lr *limitedReader) Read(p []byte) (n int, err error) {
	if lr.remaining <= 0 {
		return 0, io.EOF
	}
	if len(p) > lr.remaining {
		p = p[:lr.remaining]
	}
	n, err = lr.r.Read(p)
	lr.remaining -= n
	return
}
