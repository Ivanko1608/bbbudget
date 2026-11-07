package app

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"bbbudget/internal/budget"
	"bbbudget/internal/i18n"
	"bbbudget/internal/money"
	"bbbudget/internal/store"
)

const wrapperPath = "/wrapper/path"

const opName = string(budget.OpSetItem)

func getEnglish() *i18n.Localizer { return i18n.For("en") }

func TestGetClientFailureIgnoresTheTextAroundARecognisedError(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		want   string
	}{
		{
			name:   "op error",
			err:    &budget.OpError{Index: 2, Op: budget.OpSetItem, Err: errors.New("x")},
			status: http.StatusBadRequest,
			want:   "op 2 (" + opName + ") was rejected",
		},
		{
			name: "op error about a missing target",
			err: &budget.OpError{
				Index: 1,
				Op:    budget.OpSetItem,
				Err:   &budget.NoMatchError{Kind: "item", Ref: "r"},
			},
			status: http.StatusBadRequest,
			want:   "op 1 (" + opName + `): no item matches "r"`,
		},
		{
			name:   "budget not found",
			err:    store.ErrNotFound,
			status: http.StatusNotFound,
			want:   "budget not found",
		},
		{
			name:   "rates off",
			err:    ErrRatesOff,
			status: http.StatusConflict,
			want:   RatesOffMessage,
		},
		{
			name:   "rate provider",
			err:    &RateError{Err: errors.New("lookup " + wrapperPath)},
			status: http.StatusBadGateway,
			want:   RateFetchFailedMessage,
		},
		{
			name:   "amount out of range",
			err:    &money.ArithmeticError{Msg: "overflow " + wrapperPath},
			status: http.StatusBadRequest,
			want:   "an amount is out of range",
		},
		{
			name:   "client written text",
			err:    NewClientError(ErrBadInput, "unknown source %q", "x"),
			status: http.StatusBadRequest,
			want:   `unknown source "x"`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wrapped := fmt.Errorf("at %s: %w", wrapperPath, c.err)

			got, ok := GetClientFailure(wrapped, getEnglish)

			if !ok {
				t.Fatal("error not recognised")
			}
			if got.Status != c.status {
				t.Errorf("status %d, want %d", got.Status, c.status)
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
