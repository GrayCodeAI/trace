package cli

// SilentError wraps an error to signal that the error message has already been
// printed to the user. main.go checks for this type to avoid duplicate output.
type SilentError struct {
	Err error
}

func (e *SilentError) Error() string {
	return e.Err.Error()
}

func (e *SilentError) Unwrap() error {
	return e.Err
}

// NewSilentError creates a SilentError wrapping the given error.
// Use this when you've already printed a user-friendly error message
// and don't want main.go to print the error again.
func NewSilentError(err error) *SilentError {
	return &SilentError{Err: err}
}
