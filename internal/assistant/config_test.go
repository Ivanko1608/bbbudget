package assistant

import (
	"errors"
	"testing"

	"bbbudget/internal/settings"
)

func assistantSettings(provider settings.AssistantProvider, model, url string) settings.Settings {
	chosen := settings.Defaults()
	chosen.AssistantProvider = provider
	chosen.AssistantModel = model
	chosen.AssistantURL = url
	return chosen
}

func TestTheSettingsChooseTheProvider(t *testing.T) {
	cases := []struct {
		name      string
		settings  settings.Settings
		wantName  settings.AssistantProvider
		wantModel string
	}{
		{
			"anthropic with its default model",
			assistantSettings(settings.AssistantAnthropic, "", ""),
			settings.AssistantAnthropic,
			DefaultAnthropicModel,
		},
		{
			"anthropic with a chosen model",
			assistantSettings(settings.AssistantAnthropic, "claude-sonnet-5-5", ""),
			settings.AssistantAnthropic,
			"claude-sonnet-5-5",
		},
		{
			"an OpenAI-compatible server",
			assistantSettings(settings.AssistantOpenAICompatible, "llama3.1", ""),
			settings.AssistantOpenAICompatible,
			"llama3.1",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			provider, err := NewProvider(c.settings)

			if err != nil {
				t.Fatal(err)
			}
			if provider.Name() != c.wantName {
				t.Errorf("provider: %s", provider.Name())
			}
			if provider.Model() != c.wantModel {
				t.Errorf("model: %s", provider.Model())
			}
		})
	}
}

func TestProvidersTheSettingsCantBuild(t *testing.T) {
	cases := []struct {
		name     string
		settings settings.Settings
		want     error
	}{
		{"off", settings.Defaults(), ErrAssistantOff},
		{
			"a server without a model",
			assistantSettings(settings.AssistantOpenAICompatible, "", ""),
			ErrNoModel,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			provider, err := NewProvider(c.settings)

			if !errors.Is(err, c.want) {
				t.Errorf("error: %v", err)
			}
			if provider != nil {
				t.Errorf("provider: %v", provider)
			}
		})
	}
}

func TestStatusSaysWhetherTheModelRunsOnThisMachine(t *testing.T) {
	cases := []struct {
		name     string
		settings settings.Settings
		want     bool
	}{
		{"default server", assistantSettings(settings.AssistantOpenAICompatible, "m", ""), true},
		{
			"localhost",
			assistantSettings(settings.AssistantOpenAICompatible, "m", "http://localhost:1234/v1"),
			true,
		},
		{
			"another machine",
			assistantSettings(settings.AssistantOpenAICompatible, "m", "https://llm.example.com"),
			false,
		},
		{"anthropic", assistantSettings(settings.AssistantAnthropic, "", ""), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status := StatusFromSettings(c.settings)

			if status.OnThisMachine != c.want {
				t.Errorf("on this machine: %v", status.OnThisMachine)
			}
		})
	}
}

func TestStatusOfAnAssistantThatIsntReady(t *testing.T) {
	status := StatusFromSettings(settings.Defaults())

	if status.Ready {
		t.Error("an assistant that is off is ready")
	}
	if status.Problem != ErrAssistantOff.Error() {
		t.Errorf("problem: %q", status.Problem)
	}
}

func TestStatusOfAnErrorWithoutAMessageIsFixedText(t *testing.T) {
	problem := getProblem(errors.New("open /wrapper/path: denied"))

	if problem != problemUnknown {
		t.Errorf("problem: %q", problem)
	}
}
