package searchwire

import (
	"context"
	"net/http"
	"strings"
	"time"
)

const (
	defaultLimit     = 10
	defaultTimeout   = 10 * time.Second
	defaultUserAgent = "searchwire/0 (+https://github.com/jahrulnr/searchwire)"
	// defaultMaxResponseBytes caps the extracted readable text returned by
	// Fetch. Callers usually want the text, not the raw HTML that produced it.
	defaultMaxResponseBytes = 2 << 20 // 2 MiB of extracted text
	// defaultMaxRawResponseBytes is a hard safety ceiling on the raw HTML /
	// plaintext body actually downloaded from the server. Callers do not
	// configure this in normal use; it exists to prevent runaway downloads
	// regardless of how large Config.MaxResponseBytes may be.
	defaultMaxRawResponseBytes = 10 << 20 // 10 MiB raw download
)

// HTTPClient is satisfied by *http.Client and lightweight test doubles.
type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

// Searcher fans out to built-in sources, merges duplicates, and ranks results.
type Searcher struct {
	httpClient         HTTPClient
	userAgent          string
	limit              int
	maxResponseSize    int64 // cap on extracted text returned by Fetch
	maxRawResponseSize int64 // hard cap on raw HTML/plaintext download
	tempDir            string
	sources            []source
	answerers          []answerProvider
	// secrets holds every credential resolved during construction so tool
	// output can redact them regardless of which response they surface in.
	secrets []string
}

// New returns a Searcher configured by cfg. Use DefaultConfig() or the zero
// value for zero-configuration behavior.
func New(cfg Config) *Searcher {
	cfg = cfg.withDefaults()
	return &Searcher{
		httpClient:         cfg.HTTPClient,
		userAgent:          cfg.UserAgent,
		limit:              cfg.Limit,
		maxResponseSize:    cfg.MaxResponseBytes,
		maxRawResponseSize: cfg.MaxRawResponseBytes,
		tempDir:            cfg.TempDir,
		sources:            sourcesFromConfig(cfg),
		answerers:          answerProvidersFromConfig(cfg),
		secrets:            secretsFromConfig(cfg),
	}
}

// secretsFromConfig collects every configured credential so tool output can
// redact them no matter which upstream response echoes them back.
func secretsFromConfig(cfg Config) []string {
	secrets := make([]string, 0, 10)
	add := func(v string) {
		if v = strings.TrimSpace(v); v != "" {
			secrets = append(secrets, v)
		}
	}
	add(cfg.Brave.resolved().apiKey)
	add(cfg.Serper.resolved().apiKey)
	add(cfg.Tavily.resolved().apiKey)
	add(cfg.GitHub.resolved().token)
	add(cfg.OpenRouter.resolved().apiKey)
	add(cfg.OpenAI.resolved().apiKey)
	add(cfg.Perplexity.resolved().apiKey)
	add(cfg.Anthropic.resolved().apiKey)
	add(cfg.XAI.resolved().apiKey)
	add(cfg.OmniRoute.resolved().apiKey)
	add(cfg.OpenCode.resolved().apiKey)
	return secrets
}

// Response is the merged output of a metasearch query.
type Response struct {
	Query   string
	Results []Result
	Errors  []SourceError
}

// Result is one ranked web/text hit after deduplication and fusion.
type Result struct {
	Title   string
	URL     string
	Snippet string
	Sources []string
	Score   float64
}

// SearchOptions controls one search call. Zero values keep the configured
// defaults.
type SearchOptions struct {
	// Limit overrides the configured result limit for this call: it caps the
	// fused results returned and bounds each source's requested count.
	// When <= 0, Config.Limit applies.
	Limit int
	// Sources restricts the query to the named registered sources. Names
	// are matched case-insensitively after trimming whitespace; unknown
	// names are ignored. When no registered source matches, the search
	// fails like a fan-out with no sources. Empty means every registered
	// source participates (merged metasearch). Callers can combine this
	// with Searcher.Sources() to implement routing policies (round-robin,
	// random, fixed provider) on top of the searcher.
	Sources []string
}

// Sources returns the names of the registered search sources in
// configuration order. Callers use it to implement routing policies
// (round-robin, random, fixed provider) via SearchOptions.Sources.
func (s *Searcher) Sources() []string {
	if s == nil {
		return nil
	}
	names := make([]string, 0, len(s.sources))
	for _, src := range s.sources {
		names = append(names, src.name())
	}
	return names
}

// Search runs the query across all built-in sources concurrently.
// When at least one source succeeds, partial failures are returned in Response.Errors.
func (s *Searcher) Search(ctx context.Context, query string) (*Response, error) {
	return s.SearchWithOptions(ctx, query, SearchOptions{})
}

// SearchWithOptions is Search with per-call options such as a different
// result limit than the configured default.
func (s *Searcher) SearchWithOptions(ctx context.Context, query string, opts SearchOptions) (*Response, error) {
	if s == nil {
		return nil, ErrNilSearcher
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, ErrEmptyQuery
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = s.limit
	}
	if limit <= 0 {
		limit = defaultLimit
	}

	sources := s.sources
	if len(opts.Sources) > 0 {
		want := make(map[string]bool, len(opts.Sources))
		for _, name := range opts.Sources {
			want[strings.ToLower(strings.TrimSpace(name))] = true
		}
		filtered := make([]source, 0, len(want))
		for _, src := range s.sources {
			if want[src.name()] {
				filtered = append(filtered, src)
			}
		}
		sources = filtered
	}

	ch := make(chan sourceOutcome, len(sources))
	for i, src := range sources {
		go func(index int, src source) {
			fetcher := newFetcher(s.httpClient, s.userAgent, s.maxRawResponseSize)
			results, err := src.search(ctx, fetcher, query, limit)
			ch <- sourceOutcome{index: index, name: src.name(), results: results, err: err}
		}(i, src)
	}

	successes := make([]sourceOutcome, 0, len(sources))
	var failures []SourceError
	for range sources {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case outcome := <-ch:
			if outcome.err != nil {
				failures = append(failures, SourceError{Source: outcome.name, Error: outcome.err.Error()})
				continue
			}
			successes = append(successes, outcome)
		}
	}

	if len(successes) == 0 {
		return nil, &SearchError{Failures: orderSourceErrors(failures, sources)}
	}

	results := fuseResults(successes, sources, limit)
	return &Response{Query: query, Results: results, Errors: orderSourceErrors(failures, sources)}, nil
}
