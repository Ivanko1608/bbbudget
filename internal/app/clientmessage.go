package app

import (
	"errors"
	"fmt"

	"bbbudget/internal/budget"
	"bbbudget/internal/i18n"
	"bbbudget/internal/money"
	"bbbudget/internal/store"
)

// InternalErrorMessage is what clients are told about an error nobody
// planned for. The error itself is logged.
const InternalErrorMessage = "internal error"

// ClientClass says how a request failed, in terms every transport can map to
// its own status.
type ClientClass string

const (
	ClassBadRequest    ClientClass = "bad_request"
	ClassNotFound      ClientClass = "not_found"
	ClassConflict      ClientClass = "conflict"
	ClassUnprocessable ClientClass = "unprocessable"
	ClassUpstream      ClientClass = "upstream"
)

// ClientFailure is all a client is told about an error: its class and a
// message that is fixed text or built from the request's own input, never
// from the text of an error, because lower layers wrap errors with paths and
// upstream details.
type ClientFailure struct {
	Class   ClientClass
	Message string
	// Fields are structured data that goes with the message, such as the
	// issues of an invalid budget.
	Fields map[string]any
}

// Body is the failure as a JSON object: the message under "error" next to the fields.
func (f ClientFailure) Body() map[string]any {
	body := map[string]any{"error": f.Message}
	for key, value := range f.Fields {
		body[key] = value
	}

	return body
}

type clientRule struct {
	matches  func(err error) bool
	describe func(err error) ClientFailure
}

// clientRules is the only list of errors a client is told about. The first
// rule that matches wins, so a bad target inside an op comes before the
// error about the target.
var clientRules = []clientRule{
	typedRule(describeOpError),
	typedRule(describeValidationError),
	typedRule(describeUndoConflict),
	sentinelRule(ErrNothingToUndo, ClassConflict),
	sentinelRule(ErrTooOld, ClassConflict),
	typedRule(describeRevisionConflict),
	sentinelRuleWithClientMessage(store.ErrNotFound, ClassNotFound),
	typedRule(describeNoMatch),
	sentinelRule(store.ErrExists, ClassConflict),
	sentinelRule(ErrRatesOff, ClassConflict),
	typedRule(describeRateError),
	sentinelRule(store.ErrBadID, ClassBadRequest),
	sentinelRuleWithClientMessage(ErrBadInput, ClassBadRequest),
	typedRule(describeArithmeticError),
	sentinelRule(store.ErrNoteNotFound, ClassNotFound),
	sentinelRule(store.ErrTooManyNotes, ClassConflict),
	sentinelRule(store.ErrNoteEmpty, ClassBadRequest),
	sentinelRule(store.ErrNoteTooLong, ClassBadRequest),
	sentinelRule(store.ErrBadNoteSource, ClassBadRequest),
	sentinelRule(store.ErrBadNoteID, ClassBadRequest),
}

// GetClientFailure returns what a client is told about err, and false when err
// is not one the client may hear about. Text that has translations is rendered
// with the localizer getLocalizer returns, which is asked only when needed.
func GetClientFailure(
	err error, getLocalizer func() *i18n.Localizer,
) (ClientFailure, bool) {
	failure, ok := getClientMessageFailure(err)
	if !ok {
		return ClientFailure{}, false
	}
	failure.Fields = getClientFields(err, getLocalizer)

	return failure, true
}

// GetClientMessage returns only the message a client is told about err, and
// false when err is not one the client may hear about.
func GetClientMessage(err error) (string, bool) {
	failure, ok := getClientMessageFailure(err)

	return failure.Message, ok
}

func getClientMessageFailure(err error) (ClientFailure, bool) {
	for _, rule := range clientRules {
		if rule.matches(err) {
			return rule.describe(err), true
		}
	}

	return ClientFailure{}, false
}

func getClientFields(err error, getLocalizer func() *i18n.Localizer) map[string]any {
	fields := map[string]any{}

	var badOp *budget.OpError
	if errors.As(err, &badOp) {
		fields["op_index"] = badOp.Index
		fields["op"] = badOp.Op
	}

	var invalid *budget.ValidationError
	if errors.As(err, &invalid) {
		l := getLocalizer()
		issues := make([]budget.Issue, len(invalid.Issues))
		for i, issue := range invalid.Issues {
			issues[i] = issue.Localized(l)
		}
		fields["issues"] = issues
	}

	var undoLater *UndoConflictError
	if errors.As(err, &undoLater) {
		LocalizeHistory(undoLater.Later, getLocalizer())
		fields["later"] = undoLater.Later
	}

	var conflict *store.ConflictError
	if errors.As(err, &conflict) {
		fields["revision"] = conflict.Actual
	}

	return fields
}

// sentinelRule tells the client the text of the sentinel itself. That text is
// fixed where the sentinel is declared; the text of a wrapper around it is
// never used.
func sentinelRule(target error, class ClientClass) clientRule {
	return clientRule{
		matches: func(err error) bool { return errors.Is(err, target) },
		describe: func(error) ClientFailure {
			return ClientFailure{Class: class, Message: target.Error()}
		},
	}
}

// sentinelRuleWithClientMessage is a sentinelRule for a sentinel that app
// wraps in a ClientError to say which input it is about; the ClientError's
// message replaces the sentinel's.
func sentinelRuleWithClientMessage(target error, class ClientClass) clientRule {
	return clientRule{
		matches: func(err error) bool { return errors.Is(err, target) },
		describe: func(err error) ClientFailure {
			message := target.Error()

			var clientErr *ClientError
			if errors.As(err, &clientErr) {
				message = clientErr.Message
			}

			return ClientFailure{Class: class, Message: message}
		},
	}
}

func typedRule[T error](describe func(T) ClientFailure) clientRule {
	return clientRule{
		matches: func(err error) bool {
			var target T
			return errors.As(err, &target)
		},
		describe: func(err error) ClientFailure {
			var target T
			errors.As(err, &target)

			return describe(target)
		},
	}
}

func describeOpError(badOp *budget.OpError) ClientFailure {
	message := fmt.Sprintf("op %d (%s) was rejected", badOp.Index, badOp.Op)

	var noMatch *budget.NoMatchError
	if errors.As(badOp, &noMatch) {
		message = fmt.Sprintf("op %d (%s): %s", badOp.Index, badOp.Op, describeNoMatchText(noMatch))
	}

	return ClientFailure{Class: ClassBadRequest, Message: message}
}

func describeNoMatch(noMatch *budget.NoMatchError) ClientFailure {
	return ClientFailure{Class: ClassNotFound, Message: describeNoMatchText(noMatch)}
}

func describeNoMatchText(noMatch *budget.NoMatchError) string {
	return fmt.Sprintf("no %s matches %q", noMatch.Kind, noMatch.Ref)
}

func describeValidationError(*budget.ValidationError) ClientFailure {
	return ClientFailure{Class: ClassUnprocessable, Message: "the budget is invalid"}
}

func describeUndoConflict(undoLater *UndoConflictError) ClientFailure {
	message := fmt.Sprintf("undoing r%d would also undo %d later change(s): "+
		"undo those first, or force it to undo them all",
		undoLater.Revision, len(undoLater.Later))

	return ClientFailure{Class: ClassConflict, Message: message}
}

func describeRevisionConflict(*store.ConflictError) ClientFailure {
	message := "the budget was changed elsewhere; reload and retry"

	return ClientFailure{Class: ClassConflict, Message: message}
}

func describeRateError(*RateError) ClientFailure {
	return ClientFailure{Class: ClassUpstream, Message: RateFetchFailedMessage}
}

func describeArithmeticError(*money.ArithmeticError) ClientFailure {
	return ClientFailure{Class: ClassBadRequest, Message: "an amount is out of range"}
}
