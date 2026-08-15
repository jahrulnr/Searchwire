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
	sources            []source
	answerers          []answerProvider
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
		sources:            sourcesFromConfig(cfg),
		answerers:          answerProvidersFromConfig(cfg),
	}
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

// Search runs the query across all built-in sources concurrently.
// When at least one source succeeds, partial failures are returned in Response.Errors.
func (s *Searcher) Search(ctx context.Context, query string) (*Response, error) {
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

	ch := make(chan sourceOutcome, len(s.sources))
	for i, src := range s.sources {
		go func(index int, src source) {
			fetcher := newFetcher(s.httpClient, s.userAgent, s.maxRawResponseSize)
			results, err := src.search(ctx, fetcher, query, s.limit)
			ch <- sourceOutcome{index: index, name: src.name(), results: results, err: err}
		}(i, src)
	}

	successes := make([]sourceOutcome, 0, len(s.sources))
	var failures []SourceError
	for range s.sources {
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
		return nil, &SearchError{Failures: orderSourceErrors(failures, s.sources)}
	}

	results := fuseResults(successes, s.sources, s.limit)
	return &Response{Query: query, Results: results, Errors: orderSourceErrors(failures, s.sources)}, nil
}
