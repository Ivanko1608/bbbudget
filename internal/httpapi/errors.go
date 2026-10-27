package httpapi

import (
	"errors"
	"net/http"

	"bbbudget/internal/app"
	"bbbudget/internal/i18n"
)

// failure is what a client is told about a failed request. Its body comes
// from app.GetClientFailure or is fixed text of this package, never the text
// of an error: lower layers wrap errors with paths and upstream details.
// Nothing in this package may call Error() (see .golangci.yml).
type failure struct {
	status int
	body   map[string]any
}

// fail answers err with what the client may be told about it. Any other
// error is logged and answered with a generic 500.
func (s *server) fail(w http.ResponseWriter, r *http.Request, err error) {
	known, ok := s.getFailure(r, err)
	if !ok {
		s.logRequestError(r, "request failed", err)
		s.writeErr(w, http.StatusInternalServerError, internalErrorMessage, nil)
		return
	}

	if known.status >= http.StatusInternalServerError {
		s.logRequestError(r, "request failed", err)
	}
	s.writeJSON(w, known.status, known.body)
}

func (s *server) getFailure(r *http.Request, err error) (failure, bool) {
	clientFailure, ok := app.GetClientFailure(err, func() *i18n.Localizer { return s.reader(r) })
	if ok {
		return failure{
			status: statusFromClass(clientFailure.Class),
			body:   clientFailure.Body(),
		}, true
	}

	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return newMessageFailure(http.StatusRequestEntityTooLarge, "request body too large"), true
	}

	var undecodable *badRequestError
	if errors.As(err, &undecodable) {
		message := "the request body is not valid for this request"

		return newMessageFailure(http.StatusBadRequest, message), true
	}

	return failure{}, false
}

func statusFromClass(class app.ClientClass) int {
	switch class {
	case app.ClassBadRequest:
		return http.StatusBadRequest
	case app.ClassNotFound:
		return http.StatusNotFound
	case app.ClassConflict:
		return http.StatusConflict
	case app.ClassUnprocessable:
		return http.StatusUnprocessableEntity
	case app.ClassUpstream:
		return http.StatusBadGateway
	default:
		return http.StatusInternalServerError
	}
}

func newMessageFailure(status int, message string) failure {
	return failure{status: status, body: map[string]any{"error": message}}
}

func (s *server) logRequestError(r *http.Request, msg string, err error) {
	s.opts.Log.Error(msg, "method", r.Method, "path", r.URL.Path, "err", err)
}

// badRequestError is a request body that could not be decoded.
type badRequestError struct{ cause error }

func (e *badRequestError) Error() string { return "bad request body" }
func (e *badRequestError) Unwrap() error { return e.cause }
