package executor

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

	// exitSIGSYS is the exit code for a process killed by SIGSYS (128 + 31),
	// which is what the seccomp filter's DEFAULT KILL produces.
	exitSIGSYS = 159

	// seccompKillMsg explains a seccomp kill in terms the submitter can act on.
	seccompKillMsg = "Killed: your program attempted a system call that is not permitted in the sandbox " +
		"(seccomp-bpf policy violation). Networking, subprocess tracing, filesystem mounting and " +
		"similar operations are blocked. See sandbox/policies/ for the allowlist.\n"
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

	// Phase 1: Compile.
	//
	// The compile pass gets its OWN limits. It used to inherit req.TimeLimitMs
	// and req.MemoryLimitKB, which are the budget for the *compiled program* —
	// so a submission asking for a 100 ms runtime limit gave g++ 100 ms to
	// compile and reported a legitimate program as a compilation timeout.
	compileReq := &domain.ExecutionRequest{
		JobID:         req.JobID,
		Language:      req.Language,
		TimeLimitMs:   int(domain.CompileTimeLimit / time.Millisecond),
		MemoryLimitKB: domain.CompileMemoryLimitKB,
	}

	compileResult, err := e.runNsjail(ctx, compileReq, configPath, workDir,
		"/usr/bin/g++", "-std=c++17", "-O2", "-o", "/tmp/work/program", "/tmp/work/code.cpp")
	if err != nil {
		return nil, fmt.Errorf("compile: %w", err)
	}
	if compileResult.Status == domain.StatusTimeout {
		// Distinguish "your code takes too long to compile" from "your program
		// ran too long", which are different user-facing problems.
		compileResult.Status = domain.StatusCompilationError
		compileResult.Stderr = "compilation exceeded the " +
			domain.CompileTimeLimit.String() + " compile time limit\n" + compileResult.Stderr
		return compileResult, nil
	}
	if compileResult.ExitCode != 0 {
		compileResult.Status = domain.StatusCompilationError
		return compileResult, nil
	}

	// Phase 2: Execute the compiled binary under the job's real limits.
	return e.runNsjail(ctx, req, configPath, workDir, "/tmp/work/program")
}

func (e *SandboxExecutor) runNsjail(
	ctx context.Context,
	req *domain.ExecutionRequest,
	configPath, workDir string,
	execArgs ...string,
) (*domain.ExecutionResult, error) {
	// Build nsjail command.
	//
	// --cgroup_mem_swap_max 0 is load-bearing, not belt-and-braces. cgroup v2's
	// memory.max limits anonymous+file memory but says nothing about swap, so on
	// any host with swap enabled a process that exceeds the limit is simply paged
	// out instead of OOM-killed. Verified on this stack: a 400 MB allocation under
	// a 64 MB limit reported memory.events max=1653 (the limit was hit repeatedly)
	// and still completed successfully by consuming swap.
	//
	// That failure mode is worse than an unenforced limit: the job appears to
	// succeed, MEMORY_LIMIT_EXCEEDED never fires, and a memory bomb turns into
	// sustained swap thrashing that degrades every other job on the node. Pinning
	// memory.swap.max to 0 makes memory.max an actual ceiling.
	memBytes := req.MemoryLimitKB * 1024
	args := []string{
		"--config", configPath,
		"--bindmount", workDir + ":/tmp/work",
		"--time_limit", fmt.Sprintf("%d", req.TimeLimitMs/1000+1),
		"--cgroup_mem_max", fmt.Sprintf("%d", memBytes),
		"--cgroup_mem_swap_max", "0",
		"--",
	}
	args = append(args, execArgs...)

	timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(req.TimeLimitMs+2000)*time.Millisecond)
	defer cancel()

	cmd := exec.CommandContext(timeoutCtx, e.nsjailPath, args...)

	// Set up process group for clean termination
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Kill the whole process group, not just nsjail, and do it WHILE the process
	// is still alive. The previous code sent SIGKILL to -pid after cmd.Run() had
	// already reaped the child, which is both useless (the group is gone) and
	// unsafe (the pid may have been recycled by then, so the signal could land on
	// an unrelated process group).
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	// Bound how long Wait blocks on a child that ignores the kill and keeps its
	// stdout pipe open.
	cmd.WaitDelay = 2 * time.Second

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
		Stdout:       truncateOutput(stdout.String(), stdout.truncated),
		Stderr:       truncateOutput(progStderr, false),
		ExitCode:     0,
		TimeUsedMs:   int(elapsed.Milliseconds()),
		MemoryUsedKB: peakRSSKB(cmd),
	}

	e.logger.Debug("nsjail execution completed",
		zap.String("job_id", req.JobID.String()),
		zap.Duration("elapsed", elapsed),
		zap.Int("exit_code", result.ExitCode),
		zap.Int("memory_used_kb", result.MemoryUsedKB),
		zap.String("nsjail_log", nsjailLog),
	)

	if timeoutCtx.Err() == context.DeadlineExceeded {
		// cmd.Cancel already SIGKILLed the process group while it was alive.
		result.Status = domain.StatusTimeout
		result.ExitCode = -1
		return result, nil
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
			switch {
			case exitErr.ExitCode() == exitSIGSYS:
				// The seccomp filter killed the process for a disallowed syscall.
				// Without this branch it surfaces as a bare RUNTIME_ERROR and the user
				// has no way to tell "my code is buggy" from "my code is not permitted
				// to do that", which is the single most confusing sandbox failure.
				result.Status = domain.StatusRuntimeError
				result.Stderr = seccompKillMsg + result.Stderr
			case elapsed >= time.Duration(req.TimeLimitMs)*time.Millisecond:
				// nsjail's own --time_limit kills with SIGKILL (137) before the
				// Go context deadline fires, so a timeout and an OOM both surface
				// as 137. Disambiguate on wall-clock: if it ran past the limit,
				// it's a timeout regardless of what isOOMKill would guess.
				result.Status = domain.StatusTimeout
			case isOOMKill(exitErr.ExitCode(), nsjailLog):
				result.Status = domain.StatusMemoryLimitExceeded
			default:
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

// peakRSSKB returns the peak resident set size, in KB, of the nsjail process and
// its reaped descendants — which includes the sandboxed program.
//
// This replaces a function that took a workDir argument, ignored it entirely,
// and read /sys/fs/cgroup/memory.peak — the *worker container's* own cgroup, not
// the job's. Under `cgroup: host` that path is the host root cgroup, which
// exposes no memory controller files at all, so the number reported to users was
// either 0 or a monotonically rising container-wide figure. Either way it had
// nothing to do with the code that ran.
//
// Caveat worth knowing: ru_maxrss is a high-water mark for the waited-for child
// subtree, so it includes nsjail's own few MB of overhead. It is an upper bound
// on the user program's peak RSS, not the cgroup's memory.peak. Reading the
// per-job cgroup would be more precise, but nsjail removes that cgroup directory
// on exit, so it is gone by the time Wait returns.
func peakRSSKB(cmd *exec.Cmd) int {
	if cmd.ProcessState == nil {
		return 0
	}
	rusage, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage)
	if !ok || rusage == nil {
		return 0
	}
	if rusage.Maxrss <= 0 {
		return 0
	}
	// On Linux ru_maxrss is already in kilobytes.
	return int(rusage.Maxrss)
}
