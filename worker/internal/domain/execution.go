package domain

import (
	"time"

	"github.com/google/uuid"
)

// ExecutionStatus represents the lifecycle state of a code execution job.
type ExecutionStatus string

const (
	StatusQueued              ExecutionStatus = "QUEUED"
	StatusCompiling           ExecutionStatus = "COMPILING"
	StatusRunning             ExecutionStatus = "RUNNING"
	StatusSuccess             ExecutionStatus = "SUCCESS"
	StatusCompilationError    ExecutionStatus = "COMPILATION_ERROR"
	StatusRuntimeError        ExecutionStatus = "RUNTIME_ERROR"
	StatusTimeout             ExecutionStatus = "TIMEOUT"
	StatusMemoryLimitExceeded ExecutionStatus = "MEMORY_LIMIT_EXCEEDED"
	StatusInternalError       ExecutionStatus = "INTERNAL_ERROR"
)

// IsTerminal returns true if the status represents a final state.
func (s ExecutionStatus) IsTerminal() bool {
	switch s {
	case StatusSuccess, StatusCompilationError, StatusRuntimeError,
		StatusTimeout, StatusMemoryLimitExceeded, StatusInternalError:
		return true
	}
	return false
}

// Language represents a supported programming language.
type Language string

const (
	LangPython Language = "python"
	LangCpp    Language = "cpp"
)

// Job represents a code execution job (received from the queue).
type Job struct {
	JobID         uuid.UUID       `json:"job_id"`
	Language      Language        `json:"language"`
	SourceCode    string          `json:"source_code"`
	Stdin         string          `json:"stdin"`
	Status        ExecutionStatus `json:"status"`
	TimeLimitMs   int             `json:"time_limit_ms"`
	MemoryLimitKB int             `json:"memory_limit_kb"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

// ExecutionRequest is passed to the sandbox executor.
type ExecutionRequest struct {
	JobID         uuid.UUID
	Language      Language
	SourceCode    string
	Stdin         string
	TimeLimitMs   int
	MemoryLimitKB int
}

// ExecutionResult is returned by the sandbox executor after execution completes.
type ExecutionResult struct {
	Stdout       string
	Stderr       string
	ExitCode     int
	Status       ExecutionStatus
	TimeUsedMs   int
	MemoryUsedKB int
}
