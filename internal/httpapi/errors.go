package httpapi

import (
	"errors"
	"net/http"

	"bbbudget/internal/app"
	"bbbudget/internal/budget"
	"bbbudget/internal/store"
)

// failure is what a client is told about a failed request. Its message comes
// from app.GetClientFailure or is fixed text of this package, never the text
// of an error: lower layers wrap errors with paths and upstream details.
// Nothing in this package may call Error() (see .golangci.yml).
type failure struct {
	status  int
	message string
	// fields are extra data the client needs, such as the issues of an invalid budget.
	fields map[string]any
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
	s.writeFailure(w, known)
}

func (s *server) getFailure(r *http.Request, err error) (failure, bool) {
	if clientFailure, ok := app.GetClientFailure(err); ok {
		return failure{
			status:  statusFromClass(clientFailure.Class),
			message: clientFailure.Message,
			fields:  s.getFailureFields(r, err),
		}, true
	}

	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return failure{
			status:  http.StatusRequestEntityTooLarge,
			message: "request body too large",
		}, true
	}

	var undecodable *badRequestError
	if errors.As(err, &undecodable) {
		return failure{
			status:  http.StatusBadRequest,
			message: "the request body is not valid for this request",
		}, true
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

// getFailureFields returns the structured data a client gets next to the message.
func (s *server) getFailureFields(r *http.Request, err error) map[string]any {
	fields := map[string]any{}

	var badOp *budget.OpError
	if errors.As(err, &badOp) {
		fields["op_index"] = badOp.Index
		fields["op"] = badOp.Op
	}

	var invalid *budget.ValidationError
	if errors.As(err, &invalid) {
		reader := s.reader(r)
		issues := make([]budget.Issue, len(invalid.Issues))
		for i, issue := range invalid.Issues {
			issues[i] = issue.Localized(reader)
		}
		fields["issues"] = issues
	}

	var undoLater *app.UndoConflictError
	if errors.As(err, &undoLater) {
		app.LocalizeHistory(undoLater.Later, s.reader(r))
		fields["later"] = undoLater.Later
	}

	var conflict *store.ConflictError
	if errors.As(err, &conflict) {
		fields["revision"] = conflict.Actual
	}

	return fields
}

func (s *server) writeFailure(w http.ResponseWriter, f failure) {
	body := map[string]any{"error": f.message}
	for key, value := range f.fields {
		body[key] = value
	}
	s.writeJSON(w, f.status, body)
}

func (s *server) logRequestError(r *http.Request, msg string, err error) {
	s.opts.Log.Error(msg, "method", r.Method, "path", r.URL.Path, "err", err)
}

// badRequestError is a request body that could not be decoded.
type badRequestError struct{ cause error }

func (e *badRequestError) Error() string { return "bad request body" }
func (e *badRequestError) Unwrap() error { return e.cause }
