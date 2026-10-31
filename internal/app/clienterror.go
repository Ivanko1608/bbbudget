package app

import "fmt"

// ClientError is an error whose message was written for the client, built
// only from the request's own input. It matches Kind under errors.Is.
type ClientError struct {
	Kind    error
	Message string
	// Cause is the error the message was made from, if any, for errors.Is.
	Cause error
}

// NewClientError returns a ClientError of the given kind with a formatted message.
func NewClientError(kind error, format string, args ...any) *ClientError {
	return &ClientError{Kind: kind, Message: fmt.Sprintf(format, args...)}
}

// NewClientErrorFrom returns a ClientError of the given kind whose message is
// the text of cause. Use it only for causes that are validation messages
// written for the user, never for I/O or library errors.
func NewClientErrorFrom(kind, cause error) *ClientError {
	return &ClientError{Kind: kind, Message: cause.Error(), Cause: cause}
}

func (e *ClientError) Error() string { return e.Kind.Error() + ": " + e.Message }

func (e *ClientError) Unwrap() []error {
	if e.Cause == nil {
		return []error{e.Kind}
	}

	return []error{e.Kind, e.Cause}
}
