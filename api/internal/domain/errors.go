package domain

import "errors"

var (
	// ErrJobNotFound is returned when a job cannot be found by ID.
	ErrJobNotFound = errors.New("job not found")

	// ErrInvalidLanguage is returned when an unsupported language is submitted.
	ErrInvalidLanguage = errors.New("invalid or unsupported language")

	// ErrPayloadTooLarge is returned when the source code exceeds the size limit.
	ErrPayloadTooLarge = errors.New("source code payload exceeds maximum size (1MB)")

	// ErrEmptySourceCode is returned when source code is empty.
	ErrEmptySourceCode = errors.New("source code cannot be empty")

	// ErrRateLimitExceeded is returned when API rate limit is hit.
	ErrRateLimitExceeded = errors.New("rate limit exceeded, try again later")

	// ErrPublishFailed is returned when the message broker publish fails.
	ErrPublishFailed = errors.New("failed to publish job to message queue")

	// ErrDatabaseUnavailable is returned when the database is unreachable.
	ErrDatabaseUnavailable = errors.New("database is currently unavailable")
)
