package cmd

import "errors"

// exitError is an error that ends the process with a specific exit code instead of 1.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

// ExitCode is the process exit code for the error a command returned: 0 for none, the code of an exitError, else 1.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var e *exitError
	if errors.As(err, &e) {
		return e.code
	}
	return 1
}
