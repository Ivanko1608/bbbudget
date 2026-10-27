package app

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"bbbudget/internal/budget"
	"bbbudget/internal/i18n"
	"bbbudget/internal/money"
	"bbbudget/internal/store"
)

const wrapperPath = "/wrapper/path"

func getEnglish() *i18n.Localizer { return i18n.For("en") }

func TestGetClientFailureIgnoresTheTextAroundARecognisedError(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		class ClientClass
		want  string
	}{
		{
			"op error",
			&budget.OpError{Index: 2, Op: "set_item", Err: errors.New("x")},
			ClassBadRequest,
			"op 2 (set_item) was rejected",
		},
		{
			"op error about a missing target",
			&budget.OpError{Index: 1, Op: "set_item", Err: &budget.NoMatchError{Kind: "item", Ref: "r"}},
			ClassBadRequest,
			`op 1 (set_item): no item matches "r"`,
		},
		{"budget not found", store.ErrNotFound, ClassNotFound, "budget not found"},
		{"rates off", ErrRatesOff, ClassConflict, RatesOffMessage},
		{
			"rate provider",
			&RateError{Err: errors.New("lookup " + wrapperPath)},
			ClassUpstream,
			RateFetchFailedMessage,
		},
		{
			"amount out of range",
			&money.ArithmeticError{Msg: "overflow " + wrapperPath},
			ClassBadRequest,
			"an amount is out of range",
		},
		{
			"client written text",
			NewClientError(ErrBadInput, "unknown source %q", "x"),
			ClassBadRequest,
			`unknown source "x"`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wrapped := fmt.Errorf("at %s: %w", wrapperPath, c.err)

			got, ok := GetClientFailure(wrapped, getEnglish)

			if !ok {
				t.Fatal("error not recognised")
			}
			if got.Class != c.class {
				t.Errorf("class %q, want %q", got.Class, c.class)
			}
			if got.Message != c.want {
				t.Errorf("message %q, want %q", got.Message, c.want)
			}
			if strings.Contains(got.Message, wrapperPath) {
				t.Errorf("message leaks the wrapper: %s", got.Message)
			}
		})
	}
}

func TestGetClientMessageRefusesAnUnrecognisedError(t *testing.T) {
	_, ok := GetClientMessage(errors.New("open " + wrapperPath))

	if ok {
		t.Error("unrecognised error was given a message")
	}
}

func TestClientErrorWithoutACauseUnwrapsToItsKindOnly(t *testing.T) {
	clientErr := NewClientError(ErrBadInput, "x")

	unwrapped := clientErr.Unwrap()

	if len(unwrapped) != 1 {
		t.Errorf("unwrapped %d errors, want 1", len(unwrapped))
	}
}
