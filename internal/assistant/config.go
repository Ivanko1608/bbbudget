package assistant

import (
	"errors"

	"bbbudget/internal/settings"
)

// ErrOff means the assistant is turned off in the settings.
var ErrOff = errors.New("the assistant is off; turn it on in Settings")

// FromSettings builds the provider the settings choose.
func FromSettings(s settings.Settings) (Provider, error) {
	switch s.AssistantProvider {
	case settings.AssistantAnthropic:
		return NewAnthropic(s.AssistantAPIKey, s.AssistantModel), nil
	case settings.AssistantLocal:
		return NewLocal(s.AssistantURL, s.AssistantModel, s.AssistantAPIKey)
	}
	return nil, ErrOff
}

// Status describes the configured assistant for the UI. The key is never
// included.
type Status struct {
	Enabled  bool   `json:"enabled"`
	Provider string `json:"provider"`
	Model    string `json:"model,omitempty"`
	// Local is true when the model server is on this machine, so nothing
	// leaves it.
	Local bool `json:"local"`
}

// StatusOf reports what FromSettings would build.
func StatusOf(s settings.Settings) Status {
	p, err := FromSettings(s)
	if err != nil {
		return Status{Provider: s.AssistantProvider}
	}
	st := Status{Enabled: true, Provider: p.Name(), Model: p.Model()}
	if l, ok := p.(*Local); ok {
		st.Local = isLoopback(l.baseURL)
	}
	return st
}
