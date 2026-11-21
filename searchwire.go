package searchwire

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	defaultLimit         = 10
	defaultTimeout       = 10 * time.Second
	defaultAnswerTimeout = 60 * time.Second
	defaultUserAgent     = "searchwire/0 (+https://github.com/jahrulnr/searchwire)"
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
	answerHTTPClient   HTTPClient
	userAgent          string
	limit              int
	maxResponseSize    int64 // cap on extracted text returned by Fetch
	maxRawResponseSize int64 // hard cap on raw HTML/plaintext download
	tempDir            string
	sources            []source
	// embedder is the optional static embedding model for semantic
	// reranking; embeddingErr records why a configured model is unusable
	// (surfaced as a search warning).
	embedder     *staticEmbedder
	embeddingErr error
	// defaultSources names the sources a search without explicit
	// SearchOptions.Sources fans out to.
	defaultSources map[string]bool
	answerers      []answerProvider
	// secrets holds every credential resolved during construction so tool
	// output can redact them regardless of which response they surface in.
	secrets []string
}

// New returns a Searcher configured by cfg. Use DefaultConfig() or the zero
// value for zero-configuration behavior.
func New(cfg Config) *Searcher {
	answerTimeout := cfg.AnswerTimeout
	if answerTimeout <= 0 {
		answerTimeout = cfg.Timeout
	}
	if answerTimeout <= 0 {
		answerTimeout = defaultAnswerTimeout
	}
	customHTTPClient := cfg.HTTPClient != nil
	cfg = cfg.withDefaults()
	answerHTTPClient := cfg.HTTPClient
	if !customHTTPClient {
		answerHTTPClient = &http.Client{Timeout: answerTimeout}
	}
	sources := sourcesFromConfig(cfg)
	var embedder *staticEmbedder
	var embeddingErr error
	if dir := cfg.Embedding.modelDir(); dir != "" {
		if embedder, embeddingErr = loadStaticEmbedderCached(dir); embeddingErr != nil {
			embeddingErr = fmt.Errorf("embedding model %s unavailable, using lexical relevance only: %w", dir, embeddingErr)
		}
	}
	return &Searcher{
		embedder:           embedder,
		embeddingErr:       embeddingErr,
		defaultSources:     defaultSourceSet(sources, cfg.DefaultSources),
		httpClient:         cfg.HTTPClient,
		answerHTTPClient:   answerHTTPClient,
		userAgent:          cfg.UserAgent,
		limit:              cfg.Limit,
		maxResponseSize:    cfg.MaxResponseBytes,
		maxRawResponseSize: cfg.MaxRawResponseBytes,
		tempDir:            cfg.TempDir,
		sources:            sources,
		answerers:          answerProvidersFromConfig(cfg),
		secrets:            secretsFromConfig(cfg),
	}
}

// optInSource marks a source that is registered but excluded from the
// default fan-out (e.g. scholarly indexes): it runs only when named in
// SearchOptions.Sources or Config.DefaultSources.
type optInSource interface {
	optIn() bool
}

func isOptIn(src source) bool {
	o, ok := src.(optInSource)
	return ok && o.optIn()
}

// defaultSourceSet resolves Config.DefaultSources against the registered
// sources. It returns nil (meaning: every non-opt-in source) when no names
// are configured or none match.
func defaultSourceSet(sources []source, names []string) map[string]bool {
	want := make(map[string]bool, len(names))
	for _, name := range names {
		if name = strings.ToLower(strings.TrimSpace(name)); name != "" {
			want[name] = true
		}
	}
	var set map[string]bool
	for _, src := range sources {
		if want[src.name()] {
			if set == nil {
				set = make(map[string]bool, len(want))
			}
			set[src.name()] = true
		}
	}
	return set
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
	add(cfg.StackExchange.resolved().apiKey)
	add(cfg.OpenAlex.apiKey())
	add(cfg.SemanticScholar.apiKey())
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
	// Warnings carries non-fatal notes about the request itself, such as
	// unknown source names that were ignored.
	Warnings []string
}

// Result is one ranked web/text hit after deduplication and fusion.
type Result struct {
	Title   string
	URL     string
	Snippet string
	Sources []string
	// Score is the reciprocal-rank-fusion weight of the hit normalised
	// against the top result of this query (top = 1.0). It is a relative
	// rank within this result set — it orders hits and reflects how many
	// sources corroborated — not an absolute relevance measure; a query
	// with no good hits still yields a top score of 1.0.
	Score float64
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
	// names are ignored and reported in Response.Warnings. When no
	// requested name is registered, the default sources run instead (also
	// warned). Empty means the default sources participate. Callers can combine this
	// with Searcher.Sources() to implement routing policies (round-robin,
	// random, fixed provider) on top of the searcher.
	Sources []string
}

// Sources returns the names of every registered search source, including
// opt-in ones, in configuration order. Callers use it to implement routing
// policies (round-robin, random, fixed provider) via SearchOptions.Sources.
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

// DefaultSources returns the names of the sources a search fans out to when
// SearchOptions.Sources is empty, in configuration order.
func (s *Searcher) DefaultSources() []string {
	if s == nil {
		return nil
	}
	names := make([]string, 0, len(s.sources))
	for _, src := range s.sources {
		if s.isDefault(src) {
			names = append(names, src.name())
		}
	}
	return names
}

func (s *Searcher) isDefault(src source) bool {
	if s.defaultSources == nil {
		return !isOptIn(src)
	}
	return s.defaultSources[src.name()]
}

// Search runs the query across the default sources concurrently.
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

	sources := make([]source, 0, len(s.sources))
	for _, src := range s.sources {
		if s.isDefault(src) {
			sources = append(sources, src)
		}
	}
	var warnings []string
	if s.embeddingErr != nil {
		warnings = append(warnings, s.embeddingErr.Error())
	}
	if len(opts.Sources) > 0 {
		registered := make(map[string]source, len(s.sources))
		for _, src := range s.sources {
			registered[src.name()] = src
		}
		want := make(map[string]bool, len(opts.Sources))
		var unknown []string
		for _, raw := range opts.Sources {
			name := strings.ToLower(strings.TrimSpace(raw))
			if name == "" {
				continue
			}
			if _, ok := registered[name]; !ok {
				unknown = append(unknown, strconv.Quote(strings.TrimSpace(raw)))
				continue
			}
			want[name] = true
		}
		filtered := make([]source, 0, len(want))
		for _, src := range s.sources {
			if want[src.name()] {
				filtered = append(filtered, src)
			}
		}
		if len(unknown) > 0 {
			msg := "unknown source " + strings.Join(unknown, ", ") + " ignored"
			if len(filtered) == 0 {
				msg += "; no requested source is registered, searched the default sources instead"
			}
			warnings = append(warnings, msg+" (available: "+strings.Join(s.Sources(), ", ")+")")
		}
		if len(filtered) > 0 {
			sources = filtered
		}
	}

	ch := make(chan sourceOutcome, len(sources))
	for i, src := range sources {
		go func(index int, src source) {
			fetcher := newFetcher(s.httpClient, s.userAgent, s.maxRawResponseSize)
			results, err := src.search(ctx, fetcher, query, limit)
			ch <- sourceOutcome{index: index, name: src.name(), results: results, err: err}
		}(i, src)
	}

	// Every source runs in its own goroutine, so wall time is the slowest
	// source rather than the sum. When the caller's deadline expires first,
	// keep what already arrived: one stalled source must not discard the
	// others' results. Sources still pending are reported as failures.
	successes := make([]sourceOutcome, 0, len(sources))
	var failures []SourceError
	done := make([]bool, len(sources))
collect:
	for range sources {
		select {
		case <-ctx.Done():
			if len(successes) == 0 || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, ctx.Err()
			}
			for i, src := range sources {
				if !done[i] {
					failures = append(failures, SourceError{Source: src.name(), Error: "timed out before responding"})
				}
			}
			break collect
		case outcome := <-ch:
			done[outcome.index] = true
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

	results := fuseResults(query, s.embedder, successes, sources, limit)
	return &Response{Query: query, Results: results, Errors: orderSourceErrors(failures, sources), Warnings: warnings}, nil
}
