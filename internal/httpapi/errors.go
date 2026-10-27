package httpapi

import (
	"errors"
	"fmt"
	"net/http"

	"bbbudget/internal/app"
	"bbbudget/internal/budget"
	"bbbudget/internal/money"
	"bbbudget/internal/store"
)

// failure is what a client is told about a failed request. Its message is
// fixed text or built from fields of the request's own input, never from the
// text of an error: lower layers wrap errors with paths and upstream details.
type failure struct {
	status  int
	message string
	// fields are extra data the client needs, such as the issues of an invalid budget.
	fields map[string]any
}

type knownError struct {
	matches  func(err error) bool
	describe func(s *server, r *http.Request, err error) failure
}

// knownErrors is the only list of errors a client is told about. fail tries
// the entries in order; an error on no entry is logged and answered 500.
// Nothing in this package may call Error() (see .golangci.yml).
var knownErrors = []knownError{
	// first: a bad target inside an op is the caller's mistake
	typed(describeOpError),
	typed(describeValidationError),
	typed(describeUndoConflict),
	sentinel(app.ErrNothingToUndo, http.StatusConflict, "nothing to undo: that change created the budget"),
	sentinel(app.ErrTooOld, http.StatusConflict,
		"can't undo that far back: the history no longer keeps the version before that change"),
	typed(describeRevisionConflict),
	sentinelWithDetail(store.ErrNotFound, http.StatusNotFound, "budget not found"),
	typed(describeNoMatch),
	sentinel(store.ErrExists, http.StatusConflict, "a budget with that id already exists"),
	sentinel(app.ErrRatesOff, http.StatusConflict,
		"fetching exchange rates is turned off in Settings; type rates by hand or turn it on"),
	typed(describeRateError),
	typed(func(*server, *http.Request, *http.MaxBytesError) failure {
		return failure{status: http.StatusRequestEntityTooLarge, message: "request body too large"}
	}),
	typed(func(*server, *http.Request, *badRequestError) failure {
		return failure{status: http.StatusBadRequest, message: "the request body is not valid for this request"}
	}),
	sentinel(store.ErrBadID, http.StatusBadRequest,
		"budget id must be 1-64 lowercase letters, digits or '-', starting with a letter or digit"),
	sentinelWithDetail(app.ErrBadInput, http.StatusBadRequest, "bad input"),
	typed(func(*server, *http.Request, *money.ArithmeticError) failure {
		return failure{status: http.StatusBadRequest, message: "an amount is out of range"}
	}),

	sentinel(store.ErrNoteNotFound, http.StatusNotFound, "note not found"),
	sentinel(store.ErrTooManyNotes, http.StatusConflict,
		fmt.Sprintf("at most %d notes can be kept; delete one first", store.MaxNotes)),
	sentinel(store.ErrNoteEmpty, http.StatusBadRequest, "a note needs some text"),
	sentinel(store.ErrNoteTooLong, http.StatusBadRequest,
		fmt.Sprintf("a note is at most %d characters", store.MaxNoteRunes)),
	sentinel(store.ErrBadNoteSource, http.StatusBadRequest, "unknown note source"),
	sentinel(store.ErrBadNoteID, http.StatusBadRequest, "note id must be 16 lowercase hex characters"),
}

// fail answers err with the failure of the first known error it matches. Any
// other error is logged and answered with a generic 500.
func (s *server) fail(w http.ResponseWriter, r *http.Request, err error) {
	for _, known := range knownErrors {
		if known.matches(err) {
			s.writeFailure(w, known.describe(s, r, err))
			return
		}
	}
	s.logRequestError(r, "request failed", err)
	s.writeErr(w, http.StatusInternalServerError, internalErrorMessage, nil)
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

func sentinel(target error, status int, message string) knownError {
	return knownError{
		matches: func(err error) bool { return errors.Is(err, target) },
		describe: func(*server, *http.Request, error) failure {
			return failure{status: status, message: message}
		},
	}
}

// sentinelWithDetail is a sentinel that app wraps in a ClientError to say
// which input it is about; the ClientError's message replaces the default.
func sentinelWithDetail(target error, status int, defaultMessage string) knownError {
	return knownError{
		matches: func(err error) bool { return errors.Is(err, target) },
		describe: func(_ *server, _ *http.Request, err error) failure {
			return failure{status: status, message: detailOr(err, defaultMessage)}
		},
	}
}

// detailOr is the message of the ClientError in err's chain, else fallback.
func detailOr(err error, fallback string) string {
	var clientErr *app.ClientError
	if errors.As(err, &clientErr) {
		return clientErr.Message
	}
	return fallback
}

func typed[T error](describe func(*server, *http.Request, T) failure) knownError {
	return knownError{
		matches: func(err error) bool {
			var target T
			return errors.As(err, &target)
		},
		describe: func(s *server, r *http.Request, err error) failure {
			var target T
			errors.As(err, &target)
			return describe(s, r, target)
		},
	}
}

func describeOpError(_ *server, _ *http.Request, badOp *budget.OpError) failure {
	message := fmt.Sprintf("op %d (%s) was rejected", badOp.Index, badOp.Op)
	fields := map[string]any{"op_index": badOp.Index, "op": badOp.Op}

	var noMatch *budget.NoMatchError
	if errors.As(badOp, &noMatch) {
		message = fmt.Sprintf("op %d (%s): %s", badOp.Index, badOp.Op, describeNoMatchText(noMatch))
	}

	return failure{status: http.StatusBadRequest, message: message, fields: fields}
}

func describeNoMatch(_ *server, _ *http.Request, noMatch *budget.NoMatchError) failure {
	return failure{status: http.StatusNotFound, message: describeNoMatchText(noMatch)}
}

func describeNoMatchText(noMatch *budget.NoMatchError) string {
	return fmt.Sprintf("no %s matches %q", noMatch.Kind, noMatch.Ref)
}

func describeValidationError(s *server, r *http.Request, invalid *budget.ValidationError) failure {
	reader := s.reader(r)
	issues := make([]budget.Issue, len(invalid.Issues))
	for i, issue := range invalid.Issues {
		issues[i] = issue.Localized(reader)
	}

	return failure{
		status:  http.StatusUnprocessableEntity,
		message: "the budget is invalid",
		fields:  map[string]any{"issues": issues},
	}
}

func describeUndoConflict(s *server, r *http.Request, undoLater *app.UndoConflictError) failure {
	app.LocalizeHistory(undoLater.Later, s.reader(r))

	return failure{
		status: http.StatusConflict,
		message: fmt.Sprintf("undoing r%d would also undo %d later change(s): "+
			"undo those first, or force it to undo them all",
			undoLater.Revision, len(undoLater.Later)),
		fields: map[string]any{"later": undoLater.Later},
	}
}

func describeRevisionConflict(_ *server, _ *http.Request, conflict *store.ConflictError) failure {
	return failure{
		status:  http.StatusConflict,
		message: "the budget was changed elsewhere; reload and retry",
		fields:  map[string]any{"revision": conflict.Actual},
	}
}

func describeRateError(s *server, r *http.Request, rateFailed *app.RateError) failure {
	s.logRequestError(r, "exchange rate provider failed", rateFailed.Err)

	return failure{status: http.StatusBadGateway, message: app.RateFetchFailedMessage}
}

// badRequestError is a request body that could not be decoded.
type badRequestError struct{ cause error }

func (e *badRequestError) Error() string { return "bad request body" }
func (e *badRequestError) Unwrap() error { return e.cause }
