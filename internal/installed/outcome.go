package installed

import (
	"errors"
	"io"
)

// Severity orders operation outcomes without assigning process exit codes.
type Severity uint8

const (
	SeveritySuccess Severity = iota
	SeverityConfiguration
	SeverityInstallation
	SeverityInternal
)

// OperationError preserves a cause and its operation-level severity.
type OperationError struct {
	Severity Severity
	Err      error
}

func (e *OperationError) Error() string { return e.Err.Error() }
func (e *OperationError) Unwrap() error { return e.Err }

func operationError(severity Severity, err error) error {
	if err == nil {
		return nil
	}
	var outcome *OperationError
	if errors.As(err, &outcome) && outcome.Severity >= severity {
		return err
	}
	return &OperationError{Severity: severity, Err: err}
}

// Capture output failures even when a native command or an existing diagnostic
// writer does not propagate its Write error. They outrank configuration failures.
type operationOutput struct {
	io.Writer
	err error
}

func (w *operationOutput) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	if w.err == nil {
		w.err = err
	}
	return n, err
}

func finishOperation(report *Report, resultErr *error, output *operationOutput) {
	if output.err != nil {
		*resultErr = operationError(SeverityInternal, errors.Join(*resultErr, output.err))
		report.Configuration.Severity = SeverityInternal
		return
	}
	var outcome *OperationError
	if *resultErr != nil && !errors.As(*resultErr, &outcome) {
		*resultErr = operationError(SeverityInternal, *resultErr)
		report.Configuration.Severity = SeverityInternal
	}
}
