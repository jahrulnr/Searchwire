package searchwire

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestGitHubConfigFromEnv(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_test_token")
	settings := GitHubConfig{TokenEnv: "GITHUB_TOKEN"}.resolved()
	if settings.token != "ghp_test_token" {
		t.Fatalf("token = %q", settings.token)
	}
}

func TestGitHubConfigExplicitTokenOverridesEnv(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "from-env")
	settings := GitHubConfig{Token: "from-config"}.resolved()
	if settings.token != "from-config" {
		t.Fatalf("token = %q", settings.token)
	}
}

func TestBraveConfigFromEnv(t *testing.T) {
	t.Setenv("BRAVE_SEARCH_API_KEY", "brave_test_key")
	settings := BraveConfig{APIKeyEnv: "BRAVE_SEARCH_API_KEY"}.resolved()
	if settings.apiKey != "brave_test_key" {
		t.Fatalf("api key = %q", settings.apiKey)
	}
}

func TestBraveConfigExplicitAPIKeyOverridesEnv(t *testing.T) {
	t.Setenv("BRAVE_SEARCH_API_KEY", "from-env")
	settings := BraveConfig{APIKey: "from-config"}.resolved()
	if settings.apiKey != "from-config" {
		t.Fatalf("api key = %q", settings.apiKey)
	}
}

func TestOpenRouterConfigResolvesDefaultsAndEnvironment(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "openrouter_test_key")
	t.Setenv("OPENROUTER_MODEL", "")
	settings := (OpenRouterConfig{}).resolved()
	if settings.apiKey != "openrouter_test_key" || settings.model != "openrouter/auto" {
		t.Fatalf("settings = %#v", settings)
	}
}

func TestOpenAIConfigResolvesDefaultsAndEnvironment(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "openai_test_key")
	t.Setenv("OPENAI_MODEL", "")
	settings := (OpenAIConfig{}).resolved()
	if settings.apiKey != "openai_test_key" || settings.model != "gpt-5.6-luna" {
		t.Fatalf("settings = %#v", settings)
	}
}

func TestPerplexityConfigResolvesDefaultsAndEnvironment(t *testing.T) {
	t.Setenv("PERPLEXITY_API_KEY", "perplexity_test_key")
	t.Setenv("PERPLEXITY_PRESET", "")
	settings := (PerplexityConfig{}).resolved()
	if settings.apiKey != "perplexity_test_key" || settings.preset != "low" {
		t.Fatalf("settings = %#v", settings)
	}
}

func TestAnthropicConfigResolvesDefaultsAndEnvironment(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "anthropic_test_key")
	t.Setenv("ANTHROPIC_MODEL", "")
	settings := (AnthropicConfig{}).resolved()
	if settings.apiKey != "anthropic_test_key" || settings.model != "claude-haiku-4-5" {
		t.Fatalf("settings = %#v", settings)
	}
}

func TestXAIConfigResolvesDefaultsAndEnvironment(t *testing.T) {
	t.Setenv("XAI_API_KEY", "xai_test_key")
	t.Setenv("XAI_MODEL", "")
	settings := (XAIConfig{}).resolved()
	if settings.apiKey != "xai_test_key" || settings.model != "grok-4.5" {
		t.Fatalf("settings = %#v", settings)
	}
}

func TestLLMProviderExplicitValuesOverrideEnvironment(t *testing.T) {
	t.Setenv("CUSTOM_OPENROUTER_KEY", "from-env")
	openrouter := (OpenRouterConfig{
		APIKey:    "from-config",
		APIKeyEnv: "CUSTOM_OPENROUTER_KEY",
		Model:     "anthropic/claude-sonnet-4.6",
	}).resolved()
	if openrouter.apiKey != "from-config" || openrouter.model != "anthropic/claude-sonnet-4.6" {
		t.Fatalf("openrouter settings = %#v", openrouter)
	}

	t.Setenv("CUSTOM_OPENAI_KEY", "from-env")
	openai := (OpenAIConfig{
		APIKey:    "from-config",
		APIKeyEnv: "CUSTOM_OPENAI_KEY",
		Model:     "gpt-5.6-terra",
	}).resolved()
	if openai.apiKey != "from-config" || openai.model != "gpt-5.6-terra" {
		t.Fatalf("openai settings = %#v", openai)
	}
}

func TestBraveDisabledRemovesSource(t *testing.T) {
	disabled := false
	s := New(Config{Brave: BraveConfig{Enabled: &disabled}})
	if got := sourceNames(s.sources); containsString(got, "brave") {
		t.Fatalf("sources = %#v", got)
	}
}

func TestBraveAPIKeyReplacesHTMLSource(t *testing.T) {
	s := New(Config{Brave: BraveConfig{APIKey: "brave_test_key"}})
	if len(s.sources) != 4 {
		t.Fatalf("sources = %d", len(s.sources))
	}
	brave, ok := s.sources[0].(braveSource)
	if !ok || brave.apiKey != "brave_test_key" {
		t.Fatalf("brave source = %#v", s.sources[0])
	}
}

func TestGitHubDisabledRemovesSource(t *testing.T) {
	disabled := false
	s := New(Config{GitHub: GitHubConfig{Enabled: &disabled}})
	if len(s.sources) != 3 {
		t.Fatalf("sources = %d", len(s.sources))
	}
}

func TestGitHubReposOnlyMode(t *testing.T) {
	issues := false
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/search/issues") {
			t.Fatal("issues endpoint should not be called in repos-only mode")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(readFixtureString(t, "github-repos.json"))),
		}, nil
	})}
	s := New(Config{
		HTTPClient: client,
		GitHub:     GitHubConfig{SearchIssues: &issues},
	})
	if len(s.sources) != 4 {
		t.Fatalf("sources = %d", len(s.sources))
	}
	if _, err := s.Search(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubTokenSentAsBearer(t *testing.T) {
	var auth string
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.Host, "api.github.com") {
			auth = r.Header.Get("Authorization")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"items":[]}`)),
		}, nil
	})}
	issues := false
	s := newSearcherWithSources([]source{githubSource{
		token:        "ghp_example",
		searchIssues: issues,
	}}, Config{HTTPClient: client})
	if _, err := s.Search(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer ghp_example" {
		t.Fatalf("authorization = %q", auth)
	}
}

func TestDefaultConfigZeroValue(t *testing.T) {
	if got := DefaultConfig(); got.Limit != 0 || got.GitHub.Token != "" || got.Brave.APIKey != "" {
		t.Fatalf("default config = %#v", got)
	}
	cfg := DefaultConfig().withDefaults()
	if cfg.Limit != defaultLimit {
		t.Fatalf("limit = %d", cfg.Limit)
	}
}
