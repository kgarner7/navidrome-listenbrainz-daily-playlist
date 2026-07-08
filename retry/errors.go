package retry

import (
	"errors"
)

type Error struct {
	Error     error
	Retryable bool
}

func (e *Error) Result() (string, error) {
	if e.Retryable {
		return "", e.Error
	}

	return e.Error.Error(), nil
}

func FatalError(msg string) *Error {
	return &Error{Error: errors.New(msg), Retryable: false}
}

func TempError(err error) *Error {
	return &Error{Error: err, Retryable: true}
}
