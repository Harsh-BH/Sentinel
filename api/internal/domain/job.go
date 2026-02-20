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

// IsValid checks if the language is supported.
func (l Language) IsValid() bool {
	return l == LangPython || l == LangCpp
}

// Job represents a code execution job throughout its lifecycle.
type Job struct {
	JobID         uuid.UUID       `json:"job_id"`
	Language      Language        `json:"language"`
	SourceCode    string          `json:"source_code"`
	Stdin         string          `json:"stdin"`
	Stdout        string          `json:"stdout,omitempty"`
	Stderr        string          `json:"stderr,omitempty"`
	Status        ExecutionStatus `json:"status"`
	ExitCode      *int            `json:"exit_code,omitempty"`
	TimeUsedMs    *int            `json:"time_used_ms,omitempty"`
	MemoryUsedKB  *int            `json:"memory_used_kb,omitempty"`
	TimeLimitMs   int             `json:"time_limit_ms"`
	MemoryLimitKB int             `json:"memory_limit_kb"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

// SubmitRequest represents an incoming code submission from the API.
type SubmitRequest struct {
	Language      Language `json:"language" binding:"required"`
	SourceCode    string   `json:"source_code" binding:"required"`
	Stdin         string   `json:"stdin"`
	TimeLimitMs   *int     `json:"time_limit_ms,omitempty"`
	MemoryLimitKB *int     `json:"memory_limit_kb,omitempty"`
}

// SubmitResponse is returned after a successful submission.
type SubmitResponse struct {
	JobID  uuid.UUID `json:"job_id"`
	Status string    `json:"status"`
}

// LanguageInfo describes a supported language.
type LanguageInfo struct {
	Name     Language `json:"name"`
	Version  string   `json:"version"`
	Compiler string   `json:"compiler,omitempty"`
}
