package searchwire

import (
	"net/http"
	"os"
	"strings"
	"time"
)

// tempDirName is the per-process subdirectory created under Config.TempDir
// (or the system temp dir) when an artifact is materialized for the agent.
// Keeping all artifacts under one prefix makes cleanup and discovery easy
// (a cron / startup hook can wipe the folder).
const tempDirName = "searchwire"

// Config configures a Searcher. The zero value uses built-in defaults and
// zero-configuration sources. Optional integrations read credentials from
// explicit fields first, then from named environment variables.
type Config struct {
	HTTPClient       HTTPClient
	UserAgent        string
	Limit            int
	MaxResponseBytes int64 // budget for extracted readable text (per fetch)
	// MaxRawResponseBytes is the hard safety ceiling on the raw HTML/plaintext
	// download, independent of the caller text budget.
	MaxRawResponseBytes int64
	Timeout             time.Duration
	// AnswerTimeout applies to answer requests using the built-in HTTP client.
	// Defaults to Timeout when set, otherwise 60 seconds. A custom HTTPClient
	// controls its own timeouts and takes precedence over both values.
	AnswerTimeout time.Duration

	Brave  BraveConfig
	Serper SerperConfig
	Tavily TavilyConfig
	GitHub GitHubConfig
	// StackExchange and HackerNews are keyless developer-focused sources,
	// enabled by default.
	StackExchange StackExchangeConfig
	HackerNews    HackerNewsConfig
	// Scholarly sources (OpenAlex, arXiv, Europe PMC, Crossref, Semantic
	// Scholar) are registered but opt-in: they run only when a search names
	// them in SearchOptions.Sources or they are listed in DefaultSources.
	OpenAlex        OpenAlexConfig
	ArXiv           ArXivConfig
	EuropePMC       EuropePMCConfig
	Crossref        CrossrefConfig
	SemanticScholar SemanticScholarConfig
	// Embedding enables semantic reranking with a local static embedding
	// model. Unset means lexical relevance only.
	Embedding EmbeddingConfig
	// DefaultSources names the sources a search fans out to when the call
	// names none. Empty means every registered source except opt-in ones.
	// Unknown names are ignored; if none match, the empty default applies.
	DefaultSources []string
	// TempDir overrides the directory used to materialize truncated fetch
	// artifacts. Empty falls back to os.TempDir() + "searchwire". The
	// directory is created on demand. Each artifact is named
	// "<UTC-timestamp>-<sha256-prefix>-<safe-name>.txt" so concurrent calls
	// never collide.
	TempDir string
	// OpenRouter and OpenAI are optional answer providers. They do not add
	// metasearch sources; a resolved API key enables the answer capability.
	OpenRouter OpenRouterConfig
	OpenAI     OpenAIConfig
	Perplexity PerplexityConfig
	Anthropic  AnthropicConfig
	XAI        XAIConfig
	OmniRoute  OmniRouteConfig
	OpenCode   OpenCodeConfig
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

// SerperConfig controls the optional Serper.dev (Google Search API) source.
// Unlike Brave, Serper has no HTML fallback, so the source is only registered
// when an API key resolves and Enabled is not explicitly false (default false).
type SerperConfig struct {
	// Enabled registers the Serper source. Default false.
	Enabled *bool

	// APIKey is sent as X-API-KEY. When empty, APIKeyEnv is read.
	APIKey string
	// APIKeyEnv names the environment variable for APIKey
	// (default SERPER_API_KEY).
	APIKeyEnv string
}

// TavilyConfig controls the optional Tavily (AI search) source. The source is
// only registered when an API key resolves and Enabled is not explicitly false
// (default false).
type TavilyConfig struct {
	// Enabled registers the Tavily source. Default false.
	Enabled *bool

	// APIKey is sent as Bearer auth. When empty, APIKeyEnv is read.
	APIKey string
	// APIKeyEnv names the environment variable for APIKey
	// (default TAVILY_API_KEY).
	APIKeyEnv string
}

// StackExchangeConfig controls the built-in Stack Exchange API source
// (question/answer excerpt search). It works without a key at ~300
// requests/day per IP; a free app key raises the quota to 10,000.
type StackExchangeConfig struct {
	// Enabled registers the source. Default true.
	Enabled *bool
	// APIKey is sent as the key query parameter. When empty, APIKeyEnv is read.
	APIKey string
	// APIKeyEnv names the environment variable for APIKey
	// (default STACKEXCHANGE_API_KEY).
	APIKeyEnv string
	// Site is the Stack Exchange site parameter (default "stackoverflow"),
	// e.g. "serverfault", "unix", "math".
	Site string
}

// HackerNewsConfig controls the built-in Hacker News (Algolia HN Search API)
// story source. It needs no credentials.
type HackerNewsConfig struct {
	// Enabled registers the source. Default true.
	Enabled *bool
}

// OpenAlexConfig controls the opt-in OpenAlex scholarly source. Without a
// key, requests share a small free daily budget per IP; a free key gives
// each user its own budget.
type OpenAlexConfig struct {
	// Enabled registers the source (opt-in; see Config.DefaultSources).
	// Default true.
	Enabled *bool
	// APIKey is sent as api_key. When empty, APIKeyEnv is read.
	APIKey string
	// APIKeyEnv names the environment variable for APIKey
	// (default OPENALEX_API_KEY).
	APIKeyEnv string
}

// ArXivConfig controls the opt-in arXiv preprint source. Requests are spaced
// at least 3 seconds apart process-wide, as arXiv asks of API clients.
type ArXivConfig struct {
	// Enabled registers the source. Default true.
	Enabled *bool
}

// EuropePMCConfig controls the opt-in Europe PMC (life sciences, includes
// PubMed) source.
type EuropePMCConfig struct {
	// Enabled registers the source. Default true.
	Enabled *bool
}

// CrossrefConfig controls the opt-in Crossref journal-article source. It
// covers publishers that other indexes miss, including many regional
// journals.
type CrossrefConfig struct {
	// Enabled registers the source. Default true.
	Enabled *bool
}

// SemanticScholarConfig controls the opt-in Semantic Scholar source. It is
// registered only when an API key resolves: the keyless pool is shared by
// all anonymous clients and is usually exhausted.
type SemanticScholarConfig struct {
	// Enabled registers the source when a key resolves. Default true.
	Enabled *bool
	// APIKey is sent as X-Api-Key. When empty, APIKeyEnv is read.
	APIKey string
	// APIKeyEnv names the environment variable for APIKey
	// (default SEMANTIC_SCHOLAR_API_KEY).
	APIKeyEnv string
}

// EmbeddingConfig points at a model2vec-format static embedding model
// directory (model.safetensors, tokenizer.json, config.json), e.g.
// minishlab/potion-base-8M. The model is read once per process; nothing is
// downloaded. A model that fails to load leaves search on lexical
// relevance and adds a warning to each response.
type EmbeddingConfig struct {
	// ModelDir is the model directory. When empty, ModelDirEnv is read.
	ModelDir string
	// ModelDirEnv names the environment variable for ModelDir
	// (default SEARCHWIRE_EMBEDDING_MODEL).
	ModelDirEnv string
}

func (c EmbeddingConfig) modelDir() string {
	return keyFromConfigOrEnv(c.ModelDir, c.ModelDirEnv, "SEARCHWIRE_EMBEDDING_MODEL")
}

// OpenRouterConfig controls web-grounded answers through OpenRouter.
type OpenRouterConfig struct {
	// Enabled explicitly enables or disables the provider. When set to false,
	// env var fallback is skipped. nil means default (enabled if key resolves).
	Enabled *bool
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
	// Enabled explicitly enables or disables the provider. When set to false,
	// env var fallback is skipped. nil means default (enabled if key resolves).
	Enabled *bool
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
	// Enabled explicitly enables or disables the provider. When set to false,
	// env var fallback is skipped. nil means default (enabled if key resolves).
	Enabled *bool
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
	// Enabled explicitly enables or disables the provider. When set to false,
	// env var fallback is skipped. nil means default (enabled if key resolves).
	Enabled *bool
	// APIKey overrides APIKeyEnv (default ANTHROPIC_API_KEY).
	APIKey    string
	APIKeyEnv string
	// Model overrides ModelEnv (default ANTHROPIC_MODEL), then falls back to
	// the cost-focused Claude Haiku 4.5 model.
	Model    string
	ModelEnv string
}

// OmniRouteConfig controls web-grounded answers through the OmniRoute/9Router
// /v1/search endpoint. Unlike the other providers this is a local or
// self-hosted router, so its base URL is configurable.
type OmniRouteConfig struct {
	// Enabled explicitly enables or disables the provider. When set to false,
	// env var fallback is skipped. nil means default (enabled if key resolves).
	Enabled *bool
	// APIKey overrides APIKeyEnv (default OMNI_API_KEY).
	APIKey    string
	APIKeyEnv string
	// BaseURL overrides BaseURLEnv (default OMNI_BASE_URL), then falls back to
	// http://localhost:20128.
	BaseURL    string
	BaseURLEnv string
	// Provider pins the router's search provider selection. Empty lets the
	// router pick. ProviderEnv defaults to OMNI_SEARCH_PROVIDER.
	Provider    string
	ProviderEnv string
}

type openCodeSettings struct {
	apiKey  string
	model   string
	baseURL string
}

// OpenCodeConfig controls web-grounded answers through the OpenCode Zen
// Responses API gateway.
type OpenCodeConfig struct {
	// Enabled explicitly enables or disables the provider. When set to false,
	// env var fallback is skipped. nil means default (enabled if key resolves).
	Enabled *bool
	// APIKey overrides APIKeyEnv. The env lookup tries OPENCODE_API_KEY first,
	// then OPENCODE2_API_KEY.
	APIKey    string
	APIKeyEnv string
	// Model overrides ModelEnv (default OPENCODE_MODEL), then falls back to the
	// cost-focused gpt-5.6-luna model.
	Model    string
	ModelEnv string
	// BaseURL overrides BaseURLEnv (default OPENCODE_BASE_URL), then falls back
	// to the Zen paid lane. The free Go lane lives under /zen/go/v1 — set
	// BaseURL to https://opencode.ai/zen/go/v1 for OpenCode Go plans.
	BaseURL    string
	BaseURLEnv string
}

// XAIConfig controls web-grounded answers through the xAI Responses API.
type XAIConfig struct {
	// Enabled explicitly enables or disables the provider. When set to false,
	// env var fallback is skipped. nil means default (enabled if key resolves).
	Enabled *bool
	// APIKey overrides APIKeyEnv (default XAI_API_KEY).
	APIKey    string
	APIKeyEnv string
	// Model overrides ModelEnv (default XAI_MODEL), then falls back to
	// grok-4.5.
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
	if c.MaxRawResponseBytes <= 0 {
		c.MaxRawResponseBytes = defaultMaxRawResponseBytes
	}
	return c
}

type githubSettings struct {
	enabled      bool
	token        string
	searchIssues bool
}

const stackExchangeDefaultSite = "stackoverflow"

type stackExchangeSettings struct {
	enabled bool
	apiKey  string
	site    string
}

func (c StackExchangeConfig) resolved() stackExchangeSettings {
	settings := stackExchangeSettings{enabled: true, site: strings.TrimSpace(c.Site)}
	if c.Enabled != nil {
		settings.enabled = *c.Enabled
	}
	if settings.site == "" {
		settings.site = stackExchangeDefaultSite
	}
	settings.apiKey = strings.TrimSpace(c.APIKey)
	if settings.apiKey == "" {
		env := strings.TrimSpace(c.APIKeyEnv)
		if env == "" {
			env = "STACKEXCHANGE_API_KEY"
		}
		settings.apiKey = strings.TrimSpace(os.Getenv(env))
	}
	return settings
}

// enabledByDefault resolves an optional Enabled flag that defaults to true.
func enabledByDefault(flag *bool) bool { return flag == nil || *flag }

// keyFromConfigOrEnv returns the explicit key, else the named (or default)
// environment variable.
func keyFromConfigOrEnv(key, env, defaultEnv string) string {
	if key = strings.TrimSpace(key); key != "" {
		return key
	}
	if env = strings.TrimSpace(env); env == "" {
		env = defaultEnv
	}
	return strings.TrimSpace(os.Getenv(env))
}

func (c OpenAlexConfig) apiKey() string {
	return keyFromConfigOrEnv(c.APIKey, c.APIKeyEnv, "OPENALEX_API_KEY")
}

func (c SemanticScholarConfig) apiKey() string {
	return keyFromConfigOrEnv(c.APIKey, c.APIKeyEnv, "SEMANTIC_SCHOLAR_API_KEY")
}

func (c HackerNewsConfig) resolved() bool {
	return c.Enabled == nil || *c.Enabled
}

type braveSettings struct {
	enabled bool
	apiKey  string
}

type serperSettings struct {
	enabled bool
	apiKey  string
}

type tavilySettings struct {
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

type omniRouteSettings struct {
	apiKey   string
	baseURL  string
	provider string
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

func (s SerperConfig) resolved() serperSettings {
	settings := serperSettings{enabled: false}
	if s.Enabled != nil {
		settings.enabled = *s.Enabled
	}
	settings.apiKey = strings.TrimSpace(s.APIKey)
	if settings.apiKey == "" {
		env := strings.TrimSpace(s.APIKeyEnv)
		if env == "" {
			env = "SERPER_API_KEY"
		}
		settings.apiKey = strings.TrimSpace(os.Getenv(env))
	}
	return settings
}

func (t TavilyConfig) resolved() tavilySettings {
	settings := tavilySettings{enabled: false}
	if t.Enabled != nil {
		settings.enabled = *t.Enabled
	}
	settings.apiKey = strings.TrimSpace(t.APIKey)
	if settings.apiKey == "" {
		env := strings.TrimSpace(t.APIKeyEnv)
		if env == "" {
			env = "TAVILY_API_KEY"
		}
		settings.apiKey = strings.TrimSpace(os.Getenv(env))
	}
	return settings
}

func (c OpenRouterConfig) resolved() llmAnswerSettings {
	if c.Enabled != nil && !*c.Enabled {
		return llmAnswerSettings{}
	}
	return resolveLLMAnswerSettings(
		c.APIKey, c.APIKeyEnv, "OPENROUTER_API_KEY",
		c.Model, c.ModelEnv, "OPENROUTER_MODEL", "openrouter/auto",
	)
}

func (c OpenAIConfig) resolved() llmAnswerSettings {
	if c.Enabled != nil && !*c.Enabled {
		return llmAnswerSettings{}
	}
	settings := resolveLLMAnswerSettings(
		c.APIKey, c.APIKeyEnv, "OPENAI_API_KEY",
		c.Model, c.ModelEnv, "OPENAI_MODEL", "gpt-5.6-luna",
	)
	if settings.apiKey == "" && c.APIKey == "" && c.APIKeyEnv == "" {
		settings.apiKey = strings.TrimSpace(os.Getenv("OPENAI"))
	}
	return settings
}

func (c PerplexityConfig) resolved() perplexitySettings {
	if c.Enabled != nil && !*c.Enabled {
		return perplexitySettings{}
	}
	settings := resolveLLMAnswerSettings(
		c.APIKey, c.APIKeyEnv, "PERPLEXITY_API_KEY",
		c.Preset, c.PresetEnv, "PERPLEXITY_PRESET", "low",
	)
	return perplexitySettings{apiKey: settings.apiKey, preset: settings.model}
}

func (c AnthropicConfig) resolved() llmAnswerSettings {
	if c.Enabled != nil && !*c.Enabled {
		return llmAnswerSettings{}
	}
	return resolveLLMAnswerSettings(
		c.APIKey, c.APIKeyEnv, "ANTHROPIC_API_KEY",
		c.Model, c.ModelEnv, "ANTHROPIC_MODEL", "claude-haiku-4-5",
	)
}

func (c XAIConfig) resolved() llmAnswerSettings {
	if c.Enabled != nil && !*c.Enabled {
		return llmAnswerSettings{}
	}
	return resolveLLMAnswerSettings(
		c.APIKey, c.APIKeyEnv, "XAI_API_KEY",
		c.Model, c.ModelEnv, "XAI_MODEL", "grok-4.5",
	)
}

func (c OmniRouteConfig) resolved() omniRouteSettings {
	if c.Enabled != nil && !*c.Enabled {
		return omniRouteSettings{}
	}
	apiKey := strings.TrimSpace(c.APIKey)
	if apiKey == "" {
		env := strings.TrimSpace(c.APIKeyEnv)
		if env == "" {
			env = "OMNI_API_KEY"
		}
		apiKey = strings.TrimSpace(os.Getenv(env))
	}
	baseURL := strings.TrimSpace(c.BaseURL)
	if baseURL == "" {
		env := strings.TrimSpace(c.BaseURLEnv)
		if env == "" {
			env = "OMNI_BASE_URL"
		}
		baseURL = strings.TrimSpace(os.Getenv(env))
	}
	if baseURL == "" {
		baseURL = omniRouteDefaultBaseURL
	}
	provider := strings.TrimSpace(c.Provider)
	if provider == "" {
		env := strings.TrimSpace(c.ProviderEnv)
		if env == "" {
			env = "OMNI_SEARCH_PROVIDER"
		}
		provider = strings.TrimSpace(os.Getenv(env))
	}
	return omniRouteSettings{apiKey: apiKey, baseURL: baseURL, provider: provider}
}

func (c OpenCodeConfig) resolved() openCodeSettings {
	if c.Enabled != nil && !*c.Enabled {
		return openCodeSettings{}
	}
	settings := resolveLLMAnswerSettings(
		c.APIKey, c.APIKeyEnv, "OPENCODE_API_KEY",
		c.Model, c.ModelEnv, "OPENCODE_MODEL", "gpt-5.6-luna",
	)
	apiKey := settings.apiKey
	if apiKey == "" && c.APIKey == "" && c.APIKeyEnv == "" {
		apiKey = strings.TrimSpace(os.Getenv("OPENCODE2_API_KEY"))
	}
	if apiKey == "" {
		return openCodeSettings{}
	}
	baseURL := strings.TrimSpace(c.BaseURL)
	if baseURL == "" {
		env := c.BaseURLEnv
		if env == "" {
			env = "OPENCODE_BASE_URL"
		}
		baseURL = strings.TrimSpace(os.Getenv(env))
	}
	if baseURL == "" {
		baseURL = openCodeDefaultBaseURL
	}
	return openCodeSettings{apiKey: apiKey, model: settings.model, baseURL: baseURL}
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
	sources := make([]source, 0, 13)
	if settings := cfg.Brave.resolved(); settings.enabled {
		sources = append(sources, braveSource{apiKey: settings.apiKey})
	}
	if settings := cfg.Serper.resolved(); settings.enabled && settings.apiKey != "" {
		sources = append(sources, serperSource{apiKey: settings.apiKey})
	}
	if settings := cfg.Tavily.resolved(); settings.enabled && settings.apiKey != "" {
		sources = append(sources, tavilySource{apiKey: settings.apiKey})
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
	if settings := cfg.StackExchange.resolved(); settings.enabled {
		sources = append(sources, stackExchangeSource{apiKey: settings.apiKey, site: settings.site})
	}
	if cfg.HackerNews.resolved() {
		sources = append(sources, hackerNewsSource{})
	}
	// Opt-in scholarly sources: registered so a search can name them, but
	// outside the default fan-out unless listed in Config.DefaultSources.
	if enabledByDefault(cfg.OpenAlex.Enabled) {
		sources = append(sources, openAlexSource{apiKey: cfg.OpenAlex.apiKey()})
	}
	if key := cfg.SemanticScholar.apiKey(); key != "" && enabledByDefault(cfg.SemanticScholar.Enabled) {
		sources = append(sources, semanticScholarSource{apiKey: key})
	}
	if enabledByDefault(cfg.ArXiv.Enabled) {
		sources = append(sources, arXivSource{})
	}
	if enabledByDefault(cfg.EuropePMC.Enabled) {
		sources = append(sources, europePMCSource{})
	}
	if enabledByDefault(cfg.Crossref.Enabled) {
		sources = append(sources, crossrefSource{})
	}
	return sources
}
