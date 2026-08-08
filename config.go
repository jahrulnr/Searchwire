package searchwire

import (
	"net/http"
	"os"
	"strings"
	"time"
)

// Config configures a Searcher. The zero value uses built-in defaults and
// zero-configuration sources. Optional integrations read credentials from
// explicit fields first, then from named environment variables.
type Config struct {
	HTTPClient       HTTPClient
	UserAgent        string
	Limit            int
	MaxResponseBytes int64
	Timeout          time.Duration

	Brave  BraveConfig
	GitHub GitHubConfig
	// OpenRouter and OpenAI are optional answer providers. They do not add
	// metasearch sources; a resolved API key enables the answer capability.
	OpenRouter OpenRouterConfig
	OpenAI     OpenAIConfig
	Perplexity PerplexityConfig
	Anthropic  AnthropicConfig
	// Google and Custom are future-dev placeholders. They are not read by New()
	// until their source adapters are implemented.
	Google GoogleConfig
	Custom CustomSearchConfig
}

// BraveConfig controls the built-in Brave source. Without an API key,
// Searchwire uses Brave's public HTML results. Supplying a key switches the
// source to the official Brave Search API while preserving the source name.
type BraveConfig struct {
	// Enabled registers the Brave source. Default true.
	Enabled *bool

	// APIKey is sent as X-Subscription-Token. When empty, APIKeyEnv is read.
	APIKey string
	// APIKeyEnv names the environment variable for APIKey
	// (default BRAVE_SEARCH_API_KEY).
	APIKeyEnv string
}

// OpenRouterConfig controls web-grounded answers through OpenRouter.
type OpenRouterConfig struct {
	// APIKey overrides APIKeyEnv (default OPENROUTER_API_KEY).
	APIKey    string
	APIKeyEnv string
	// Model overrides ModelEnv (default OPENROUTER_MODEL), then falls back to
	// openrouter/auto.
	Model    string
	ModelEnv string
}

// OpenAIConfig controls web-grounded answers through the OpenAI Responses API.
type OpenAIConfig struct {
	// APIKey overrides APIKeyEnv (default OPENAI_API_KEY).
	APIKey    string
	APIKeyEnv string
	// Model overrides ModelEnv (default OPENAI_MODEL), then falls back to the
	// cost-focused gpt-5.6-luna model.
	Model    string
	ModelEnv string
}

// PerplexityConfig controls web-grounded answers through the Agent API.
type PerplexityConfig struct {
	// APIKey overrides APIKeyEnv (default PERPLEXITY_API_KEY).
	APIKey    string
	APIKeyEnv string
	// Preset overrides PresetEnv (default PERPLEXITY_PRESET), then falls back
	// to Perplexity's cost-focused low preset.
	Preset    string
	PresetEnv string
}

// AnthropicConfig controls web-grounded answers through the Messages API.
type AnthropicConfig struct {
	// APIKey overrides APIKeyEnv (default ANTHROPIC_API_KEY).
	APIKey    string
	APIKeyEnv string
	// Model overrides ModelEnv (default ANTHROPIC_MODEL), then falls back to
	// the cost-focused Claude Haiku 4.5 model.
	Model    string
	ModelEnv string
}

// GitHubConfig controls the built-in GitHub Search API source.
type GitHubConfig struct {
	// Enabled registers the GitHub source. Default true.
	Enabled *bool

	// Token is sent as Bearer auth. When empty, TokenEnv is read.
	Token string
	// TokenEnv names the environment variable for Token (default GITHUB_TOKEN).
	TokenEnv string

	// SearchIssues enables repository+issues mode. When false, only repositories
	// are searched. Default true. When true, repository search still serves as
	// the fallback if issues search fails.
	SearchIssues *bool
}

// GoogleConfig is a future-dev placeholder for Google Programmable Search Engine.
// Setting these fields has no effect on Search today.
type GoogleConfig struct {
	// APIKey is the Google Custom Search JSON API key (future).
	APIKey string
	// APIKeyEnv names the env var for APIKey (planned default: GOOGLE_API_KEY).
	APIKeyEnv string
	// CX is the programmable search engine ID (future).
	CX string
	// CXEnv names the env var for CX (planned default: GOOGLE_CX).
	CXEnv string
}

// CustomSearchConfig is a future-dev placeholder for a caller-provided search
// endpoint. Setting URL has no effect on Search today.
type CustomSearchConfig struct {
	URL string
}

// DefaultConfig returns the zero-configuration defaults.
func DefaultConfig() Config {
	return Config{}
}

func (c Config) withDefaults() Config {
	if c.HTTPClient == nil {
		timeout := c.Timeout
		if timeout <= 0 {
			timeout = defaultTimeout
		}
		c.HTTPClient = &http.Client{Timeout: timeout}
	}
	if strings.TrimSpace(c.UserAgent) == "" {
		c.UserAgent = defaultUserAgent
	}
	if c.Limit <= 0 {
		c.Limit = defaultLimit
	}
	if c.MaxResponseBytes <= 0 {
		c.MaxResponseBytes = defaultMaxResponseBytes
	}
	return c
}

type githubSettings struct {
	enabled      bool
	token        string
	searchIssues bool
}

type braveSettings struct {
	enabled bool
	apiKey  string
}

type llmAnswerSettings struct {
	apiKey string
	model  string
}

type perplexitySettings struct {
	apiKey string
	preset string
}

func (b BraveConfig) resolved() braveSettings {
	settings := braveSettings{enabled: true}
	if b.Enabled != nil {
		settings.enabled = *b.Enabled
	}
	settings.apiKey = strings.TrimSpace(b.APIKey)
	if settings.apiKey == "" {
		env := strings.TrimSpace(b.APIKeyEnv)
		if env == "" {
			env = "BRAVE_SEARCH_API_KEY"
		}
		settings.apiKey = strings.TrimSpace(os.Getenv(env))
	}
	return settings
}

func (c OpenRouterConfig) resolved() llmAnswerSettings {
	return resolveLLMAnswerSettings(
		c.APIKey, c.APIKeyEnv, "OPENROUTER_API_KEY",
		c.Model, c.ModelEnv, "OPENROUTER_MODEL", "openrouter/auto",
	)
}

func (c OpenAIConfig) resolved() llmAnswerSettings {
	return resolveLLMAnswerSettings(
		c.APIKey, c.APIKeyEnv, "OPENAI_API_KEY",
		c.Model, c.ModelEnv, "OPENAI_MODEL", "gpt-5.6-luna",
	)
}

func (c PerplexityConfig) resolved() perplexitySettings {
	settings := resolveLLMAnswerSettings(
		c.APIKey, c.APIKeyEnv, "PERPLEXITY_API_KEY",
		c.Preset, c.PresetEnv, "PERPLEXITY_PRESET", "low",
	)
	return perplexitySettings{apiKey: settings.apiKey, preset: settings.model}
}

func (c AnthropicConfig) resolved() llmAnswerSettings {
	return resolveLLMAnswerSettings(
		c.APIKey, c.APIKeyEnv, "ANTHROPIC_API_KEY",
		c.Model, c.ModelEnv, "ANTHROPIC_MODEL", "claude-haiku-4-5",
	)
}

func resolveLLMAnswerSettings(apiKey, apiKeyEnv, defaultAPIKeyEnv, model, modelEnv, defaultModelEnv, defaultModel string) llmAnswerSettings {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		if apiKeyEnv = strings.TrimSpace(apiKeyEnv); apiKeyEnv == "" {
			apiKeyEnv = defaultAPIKeyEnv
		}
		apiKey = strings.TrimSpace(os.Getenv(apiKeyEnv))
	}
	model = strings.TrimSpace(model)
	if model == "" {
		if modelEnv = strings.TrimSpace(modelEnv); modelEnv == "" {
			modelEnv = defaultModelEnv
		}
		model = strings.TrimSpace(os.Getenv(modelEnv))
	}
	if model == "" {
		model = defaultModel
	}
	return llmAnswerSettings{apiKey: apiKey, model: model}
}

func (g GitHubConfig) resolved() githubSettings {
	settings := githubSettings{
		enabled:      true,
		searchIssues: true,
	}
	if g.Enabled != nil {
		settings.enabled = *g.Enabled
	}
	if g.SearchIssues != nil {
		settings.searchIssues = *g.SearchIssues
	}
	settings.token = strings.TrimSpace(g.Token)
	if settings.token == "" {
		env := strings.TrimSpace(g.TokenEnv)
		if env == "" {
			env = "GITHUB_TOKEN"
		}
		settings.token = strings.TrimSpace(os.Getenv(env))
	}
	return settings
}

func sourcesFromConfig(cfg Config) []source {
	sources := make([]source, 0, 4)
	if settings := cfg.Brave.resolved(); settings.enabled {
		sources = append(sources, braveSource{apiKey: settings.apiKey})
	}
	sources = append(sources,
		startpageSource{},
		wikipediaSource{},
	)
	if cfg.GitHub.resolved().enabled {
		settings := cfg.GitHub.resolved()
		sources = append(sources, githubSource{
			token:        settings.token,
			searchIssues: settings.searchIssues,
		})
	}
	return sources
}
