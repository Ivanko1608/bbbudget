package assistant

import (
	"errors"
	"net"
	"net/url"

	"bbbudget/internal/app"
	"bbbudget/internal/settings"
)

// problemUnknown is what the status says when the settings fail in a way
// that has no message of its own.
const problemUnknown = "the assistant can't be set up with these settings"

// ErrAssistantOff means the assistant is turned off in the settings.
var ErrAssistantOff = errors.New("the assistant is off; turn it on in Settings")

// NewProvider builds the provider the settings choose.
func NewProvider(s settings.Settings) (Provider, error) {
	switch s.AssistantProvider {
	case settings.AssistantAnthropic:
		config := AnthropicConfig{APIKey: s.AssistantAPIKey, Model: s.AssistantModel}
		return NewAnthropic(config), nil
	case settings.AssistantOpenAICompatible:
		return newOpenAICompatibleProvider(s)
	default:
		return nil, ErrAssistantOff
	}
}

func newOpenAICompatibleProvider(s settings.Settings) (Provider, error) {
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

// ProviderFromSettings returns the provider the app's settings choose,
// reading them afresh on each call.
func ProviderFromSettings(a *app.App) func() (Provider, error) {
	return func() (Provider, error) {
		current, err := a.Settings()
		if err != nil {
			return nil, err
		}
		return NewProvider(current)
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

// StatusFromSettings reports what NewProvider would build.
func StatusFromSettings(s settings.Settings) Status {
	provider, err := NewProvider(s)
	if err != nil {
		return Status{Provider: s.AssistantProvider, Problem: getProblem(err)}
	}
	return Status{
		Ready:         true,
		Provider:      provider.Name(),
		Model:         provider.Model(),
		OnThisMachine: runsOnThisMachine(provider),
	}
}

// getProblem is what the status tells about a provider that can't be built:
// the text of the errors written for that, never the text of any other.
func getProblem(err error) string {
	switch {
	case errors.Is(err, ErrAssistantOff):
		return ErrAssistantOff.Error()
	case errors.Is(err, ErrNoModel):
		return ErrNoModel.Error()
	default:
		return problemUnknown
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
