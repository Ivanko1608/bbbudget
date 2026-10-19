package assistant

import (
	"errors"
	"net"
	"net/url"

	"bbbudget/internal/app"
	"bbbudget/internal/settings"
)

// ErrOff means the assistant is turned off in the settings.
var ErrOff = errors.New("the assistant is off; turn it on in Settings")

// FromSettings builds the provider the settings choose.
func FromSettings(s settings.Settings) (Provider, error) {
	switch s.AssistantProvider {
	case settings.AssistantAnthropic:
		config := AnthropicConfig{APIKey: s.AssistantAPIKey, Model: s.AssistantModel}
		return NewAnthropic(config), nil
	case settings.AssistantOpenAICompatible:
		return openAICompatibleFrom(s)
	default:
		return nil, ErrOff
	}
}

func openAICompatibleFrom(s settings.Settings) (Provider, error) {
	provider, err := NewOpenAICompatible(OpenAICompatibleConfig{
		BaseURL: s.AssistantURL,
		Model:   s.AssistantModel,
		APIKey:  s.AssistantAPIKey,
	})
	if err != nil {
		return nil, err
	}
	return provider, nil
}

// Configured returns the provider the app's settings choose, reading them
// afresh on each call.
func Configured(a *app.App) func() (Provider, error) {
	return func() (Provider, error) {
		current, err := a.Settings()
		if err != nil {
			return nil, err
		}
		return FromSettings(current)
	}
}

// Status describes the configured assistant for the UI. The key is never
// included.
type Status struct {
	Ready    bool                       `json:"ready"`
	Provider settings.AssistantProvider `json:"provider"`
	Model    string                     `json:"model,omitempty"`
	// OnThisMachine is true when the model server is on this machine, so
	// nothing about the budget leaves it.
	OnThisMachine bool `json:"on_this_machine"`
	// Problem says why the assistant isn't ready.
	Problem string `json:"problem,omitempty"`
}

// StatusOf reports what FromSettings would build.
func StatusOf(s settings.Settings) Status {
	provider, err := FromSettings(s)
	if err != nil {
		return Status{Provider: s.AssistantProvider, Problem: err.Error()}
	}
	return Status{
		Ready:         true,
		Provider:      provider.Name(),
		Model:         provider.Model(),
		OnThisMachine: runsOnThisMachine(provider),
	}
}

func runsOnThisMachine(provider Provider) bool {
	server, isServer := provider.(*OpenAICompatible)
	return isServer && isLoopback(server.BaseURL())
}

func isLoopback(baseURL string) bool {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	host := parsed.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
