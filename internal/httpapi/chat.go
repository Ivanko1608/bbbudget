package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"bbbudget/internal/app"
	"bbbudget/internal/assistant"
)

type chatRequest struct {
	// Conversation continues an earlier conversation; empty starts one.
	Conversation string `json:"conversation"`
	Message      string `json:"message"`
}

// chat answers with server-sent events: what the assistant says and does,
// then an error event if the reply failed, and always a done event last.
func (s *server) chat(w http.ResponseWriter, r *http.Request) {
	if s.opts.Assistant == nil {
		s.writeErr(w, http.StatusNotFound, "the assistant is not available here", nil)
		return
	}
	var body chatRequest
	if err := decode(r, &body); err != nil {
		s.fail(w, r, err)
		return
	}
	request := assistant.Request{
		Conversation: body.Conversation,
		Budget:       r.PathValue("id"),
		Message:      body.Message,
	}
	events := startEventStream(w)
	conversation, err := s.opts.Assistant.Reply(r.Context(), request, events.send)
	if err != nil {
		s.logRequestError(r, "assistant reply failed", err)
		events.send(assistant.Event{Type: assistant.EventError, Error: chatErrorMessage(err)})
	}
	events.send(assistant.Event{Type: assistant.EventDone, Conversation: conversation})
}

type eventStream struct {
	w          http.ResponseWriter
	controller *http.ResponseController
}

func startEventStream(w http.ResponseWriter) *eventStream {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	return &eventStream{w: w, controller: http.NewResponseController(w)}
}

// send ignores write errors: they mean the browser went away, and the
// request's context, cancelled with it, stops the reply.
func (s *eventStream) send(event assistant.Event) {
	encoded, err := json.Marshal(event)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(s.w, "data: %s\n\n", encoded)
	_ = s.controller.Flush()
}

const assistantFailedMessage = "the assistant couldn't answer; see the server log"

// chatErrorMessage is what the stream says about a failed reply: the request's
// own mistake in the client's terms, anything else a fixed message.
func chatErrorMessage(err error) string {
	if errors.Is(err, app.ErrBadInput) {
		return detailOr(err, "bad input")
	}
	return assistantFailedMessage
}
