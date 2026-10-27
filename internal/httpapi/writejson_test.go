package httpapi

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newLoggingServer() (*server, *bytes.Buffer) {
	var logged bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logged, nil))

	return &server{opts: Options{Log: logger}}, &logged
}

type failingWriter struct {
	http.ResponseWriter
}

var errClientGone = errors.New("client went away")

func (failingWriter) Write([]byte) (int, error) {
	return 0, errClientGone
}

func TestWriteJSONKeepsTheBytesOfTheEncoder(t *testing.T) {
	s, _ := newLoggingServer()
	w := httptest.NewRecorder()

	s.writeJSON(w, http.StatusCreated, map[string]string{"note": "<a&b>"})

	eq(t, "status", w.Code, http.StatusCreated)
	eq(t, "content type", w.Header().Get("Content-Type"), "application/json; charset=utf-8")
	eq(t, "body", w.Body.String(), `{"note":"\u003ca\u0026b\u003e"}`+"\n")
}

func TestWriteJSONOfAValueThatCannotBeEncodedIsAGeneric500(t *testing.T) {
	s, _ := newLoggingServer()
	w := httptest.NewRecorder()

	s.writeJSON(w, http.StatusOK, map[string]any{"broken": make(chan int)})

	eq(t, "status", w.Code, http.StatusInternalServerError)
	eq(t, "content type", w.Header().Get("Content-Type"), "application/json; charset=utf-8")
	eq(t, "body", w.Body.String(), `{"error":"internal error"}`+"\n")
}

func TestWriteJSONLogsAnEncodingFailure(t *testing.T) {
	s, logged := newLoggingServer()
	w := httptest.NewRecorder()

	s.writeJSON(w, http.StatusOK, map[string]any{"broken": make(chan int)})

	eq(t, "logged the failure", strings.Contains(logged.String(), "encoding response failed"), true)
	eq(t, "logged the cause", strings.Contains(logged.String(), "unsupported type"), true)
}

func TestWriteJSONLogsAFailedWrite(t *testing.T) {
	s, logged := newLoggingServer()
	w := failingWriter{httptest.NewRecorder()}

	s.writeJSON(w, http.StatusOK, map[string]string{"ok": "yes"})

	eq(t, "logged the failure", strings.Contains(logged.String(), "writing response failed"), true)
	eq(t, "logged the cause", strings.Contains(logged.String(), errClientGone.Error()), true)
}

func TestInternalErrorBodyMatchesWhatWriteErrSends(t *testing.T) {
	want, err := encodeJSON(errBody{Error: internalErrorMessage})
	if err != nil {
		t.Fatal(err)
	}

	eq(t, "fallback body", string(internalErrorBody), string(want))
}
