package retry

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
