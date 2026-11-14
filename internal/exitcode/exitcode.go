package exitcode

import (
	"errors"
	"fmt"
)

const (
	Success       = 0
	Generic       = 1
	InvalidInput  = 2
	Compatibility = 3
	Health        = 4
	BackupRestore = 5
	Network       = 6
)

// Error is a CLI failure with a deterministic exit code and optional hint.
type Error struct {
	Code int
	Err  error
	Hint string
}

func (e *Error) Error() string {
	if e == nil || e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func Wrap(code int, err error) error {
	if err == nil {
		return nil
	}
	var existing *Error
	if errors.As(err, &existing) {
		return err
	}
	return &Error{Code: code, Err: err}
}

func New(code int, format string, args ...any) error {
	return &Error{Code: code, Err: fmt.Errorf(format, args...)}
}

// NewHint is like New but attaches a next-step hint printed by the CLI.
func NewHint(code int, hint, format string, args ...any) error {
	return &Error{Code: code, Err: fmt.Errorf(format, args...), Hint: hint}
}

func CodeOf(err error) int {
	if err == nil {
		return Success
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return Generic
}

// HintOf returns an actionable next-step string when present.
func HintOf(err error) string {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Hint
	}
	return ""
}
