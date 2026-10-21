package searchwire

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewCreatesDefaultSearcher(t *testing.T) {
	s := New(Config{})
	if s == nil || len(s.sources) != 4 {
		t.Fatalf("expected default searcher with 4 sources, got %#v", s)
	}
}

func TestSearchTrimsQuery(t *testing.T) {
	var got string
	s := newSearcherWithSources([]source{fakeSource{
		nameValue: "fake",
		searchFn: func(_ context.Context, _ *fetcher, query string, _ int) ([]sourceResult, error) {
			got = query
			return []sourceResult{{title: "A", url: "https://example.com/a", rank: 1}}, nil
		},
	}}, Config{})
	resp, err := s.Search(context.Background(), "  hello  ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello" || resp.Query != "hello" {
		t.Fatalf("query = %q resp.Query = %q", got, resp.Query)
	}
}

func TestSearchRejectsEmptyQuery(t *testing.T) {
	s := New(Config{})
	if _, err := s.Search(context.Background(), "   "); !errors.Is(err, ErrEmptyQuery) {
		t.Fatalf("err = %v", err)
	}
}

func TestNilSearcherReturnsError(t *testing.T) {
	var s *Searcher
	if _, err := s.Search(context.Background(), "x"); !errors.Is(err, ErrNilSearcher) {
		t.Fatalf("err = %v", err)
	}
}

func TestSearchRunsSourcesConcurrently(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	s := newSearcherWithSources([]source{
		fakeSource{nameValue: "slow", searchFn: func(ctx context.Context, _ *fetcher, _ string, _ int) ([]sourceResult, error) {
			started <- "slow"
			select {
			case <-release:
				return []sourceResult{{title: "Slow", url: "https://example.com/slow", rank: 1}}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}},
		fakeSource{nameValue: "fast", searchFn: func(_ context.Context, _ *fetcher, _ string, _ int) ([]sourceResult, error) {
			started <- "fast"
			return []sourceResult{{title: "Fast", url: "https://example.com/fast", rank: 1}}, nil
		}},
	}, Config{})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		resp, err := s.Search(context.Background(), "test")
		if err != nil {
			t.Errorf("search failed: %v", err)
			return
		}
		if len(resp.Results) != 2 {
			t.Errorf("results = %d", len(resp.Results))
		}
	}()

	first := <-started
	second := <-started
	if first == "" || second == "" || first == second {
		t.Fatalf("expected two distinct concurrent starts, got %q and %q", first, second)
	}
	close(release)
	wg.Wait()
}

func TestSearchPartialFailure(t *testing.T) {
	s := newSearcherWithSources([]source{
		fakeSource{nameValue: "good", searchFn: okSource("https://example.com/ok")},
		fakeSource{nameValue: "bad", searchFn: func(context.Context, *fetcher, string, int) ([]sourceResult, error) {
			return nil, errors.New("boom")
		}},
	}, Config{})
	resp, err := s.Search(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 || len(resp.Errors) != 1 || resp.Errors[0].Source != "bad" {
		t.Fatalf("resp = %#v", resp)
	}
}

func TestSearchAllSourcesFail(t *testing.T) {
	s := newSearcherWithSources([]source{
		fakeSource{nameValue: "a", searchFn: failSource("a fail")},
		fakeSource{nameValue: "b", searchFn: failSource("b fail")},
	}, Config{})
	_, err := s.Search(context.Background(), "test")
	var searchErr *SearchError
	if !errors.As(err, &searchErr) {
		t.Fatalf("err = %v", err)
	}
	if len(searchErr.Failures) != 2 || searchErr.Failures[0].Source != "a" || searchErr.Failures[1].Source != "b" {
		t.Fatalf("failures = %#v", searchErr.Failures)
	}
}

func TestSearchZeroResultsIsSuccess(t *testing.T) {
	s := newSearcherWithSources([]source{fakeSource{
		nameValue: "empty",
		searchFn: func(context.Context, *fetcher, string, int) ([]sourceResult, error) {
			return nil, nil
		},
	}}, Config{})
	resp, err := s.Search(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 0 {
		t.Fatalf("results = %#v", resp.Results)
	}
}

func TestSearchContextCancellation(t *testing.T) {
	s := newSearcherWithSources([]source{fakeSource{
		nameValue: "block",
		searchFn: func(ctx context.Context, _ *fetcher, _ string, _ int) ([]sourceResult, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}}, Config{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Search(ctx, "test"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestSearchContextDeadline(t *testing.T) {
	s := newSearcherWithSources([]source{fakeSource{
		nameValue: "block",
		searchFn: func(ctx context.Context, _ *fetcher, _ string, _ int) ([]sourceResult, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}}, Config{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	time.Sleep(5 * time.Millisecond)
	if _, err := s.Search(ctx, "test"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

func TestConfigLimitTruncates(t *testing.T) {
	s := newSearcherWithSources([]source{fakeSource{
		nameValue: "many",
		searchFn: func(context.Context, *fetcher, string, int) ([]sourceResult, error) {
			return []sourceResult{
				{title: "1", url: "https://example.com/1", rank: 1},
				{title: "2", url: "https://example.com/2", rank: 2},
				{title: "3", url: "https://example.com/3", rank: 3},
			}, nil
		},
	}}, Config{Limit: 2})
	resp, err := s.Search(context.Background(), "test")
	if err != nil || len(resp.Results) != 2 {
		t.Fatalf("results = %#v err = %v", resp.Results, err)
	}
}

func TestConfigLimitIgnoresInvalid(t *testing.T) {
	s := New(Config{Limit: 0})
	if s.limit != defaultLimit {
		t.Fatalf("limit = %d", s.limit)
	}
}

func TestConfigHTTPClientUsed(t *testing.T) {
	var called atomic.Bool
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		called.Store(true)
		body := `{"query":{"pages":[{"pageid":1,"title":"Go","index":1,"extract":"x","fullurl":"https://en.wikipedia.org/wiki/Go"}]}}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
		}, nil
	})}
	s := newSearcherWithSources([]source{wikipediaSource{}}, Config{HTTPClient: client})
	if _, err := s.Search(context.Background(), "Go"); err != nil {
		t.Fatal(err)
	}
	if !called.Load() {
		t.Fatal("custom client was not used")
	}
}

type fakeSource struct {
	nameValue string
	searchFn  func(context.Context, *fetcher, string, int) ([]sourceResult, error)
}

func (f fakeSource) name() string { return f.nameValue }

func (f fakeSource) search(ctx context.Context, fetcher *fetcher, query string, limit int) ([]sourceResult, error) {
	return f.searchFn(ctx, fetcher, query, limit)
}

func okSource(url string) func(context.Context, *fetcher, string, int) ([]sourceResult, error) {
	return func(context.Context, *fetcher, string, int) ([]sourceResult, error) {
		return []sourceResult{{title: "OK", url: url, rank: 1}}, nil
	}
}

func failSource(message string) func(context.Context, *fetcher, string, int) ([]sourceResult, error) {
	return func(context.Context, *fetcher, string, int) ([]sourceResult, error) {
		return nil, errors.New(message)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func fixturePath(name string) string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "testdata", name)
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(fixturePath(name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestFuseDuplicateURLs(t *testing.T) {
	outcomes := []sourceOutcome{
		{name: "brave", results: []sourceResult{{title: "Go", url: "https://Go.Dev/", snippet: "short", rank: 1}}},
		{name: "startpage", results: []sourceResult{{title: "Go Lang", url: "https://go.dev?utm_source=test", snippet: "longer snippet", rank: 1}}},
	}
	results := fuseResults(outcomes, defaultSources(), 10)
	if len(results) != 1 {
		t.Fatalf("results = %#v", results)
	}
	if results[0].Score <= 1.0/(rrfK+1) {
		t.Fatalf("score = %v", results[0].Score)
	}
	if results[0].Snippet != "longer snippet" {
		t.Fatalf("snippet = %q", results[0].Snippet)
	}
	if len(results[0].Sources) != 2 || results[0].Sources[0] != "brave" {
		t.Fatalf("sources = %#v", results[0].Sources)
	}
}

func TestCanonicalURLNormalization(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"HTTPS://Example.COM/Path/", "https://example.com/Path"},
		{"https://example.com:443/x", "https://example.com/x"},
		{"http://example.com:80/x", "http://example.com/x"},
		{"https://example.com/x#frag", "https://example.com/x"},
		{"https://example.com/", "https://example.com/"},
		{"https://example.com/x?b=2&a=1&utm_source=x", "https://example.com/x?a=1&b=2"},
	}
	for _, tc := range cases {
		_, got, err := canonicalURL(tc.in)
		if err != nil {
			t.Fatalf("%q: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("%q = %q, want %q", tc.in, got, tc.want)
		}
	}
	_, _, err := canonicalURL("https://example.com/a?foo=1")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = canonicalURL("https://example.com/b?bar=2")
	if err != nil {
		t.Fatal(err)
	}
	keyA, _, _ := canonicalURL("https://example.com/a?foo=1")
	keyB, _, _ := canonicalURL("https://example.com/b?bar=2")
	if keyA == keyB {
		t.Fatal("distinct queries merged")
	}
}

func TestFuseDeterministicOrdering(t *testing.T) {
	outcomes := []sourceOutcome{
		{name: "brave", results: []sourceResult{{title: "A", url: "https://a.example", rank: 1}}},
		{name: "startpage", results: []sourceResult{{title: "B", url: "https://b.example", rank: 1}}},
	}
	first := fuseResults(outcomes, defaultSources(), 10)
	second := fuseResults([]sourceOutcome{outcomes[1], outcomes[0]}, defaultSources(), 10)
	if fmt.Sprintf("%#v", first) != fmt.Sprintf("%#v", second) {
		t.Fatalf("ordering differed: %#v vs %#v", first, second)
	}
}

func TestFuseLimitBoundaries(t *testing.T) {
	outcomes := []sourceOutcome{{name: "brave", results: []sourceResult{
		{title: "1", url: "https://example.com/1", rank: 1},
		{title: "2", url: "https://example.com/2", rank: 2},
	}}}
	if got := len(fuseResults(outcomes, defaultSources(), 1)); got != 1 {
		t.Fatalf("limit 1 = %d", got)
	}
	if got := len(fuseResults(outcomes, defaultSources(), defaultLimit)); got != 2 {
		t.Fatalf("default limit = %d", got)
	}
}

func TestSearchWithOptionsLimitReachesSourcesAndFusion(t *testing.T) {
	s := newSearcherWithSources([]source{fakeSource{
		nameValue: "one",
		searchFn: func(_ context.Context, _ *fetcher, _ string, limit int) ([]sourceResult, error) {
			if limit != 25 {
				t.Fatalf("source received limit %d, want 25", limit)
			}
			results := make([]sourceResult, 0, 30)
			for i := 0; i < 30; i++ {
				results = append(results, sourceResult{
					title: fmt.Sprintf("result-%02d", i),
					url:   fmt.Sprintf("https://example.com/%d", i),
					rank:  i + 1,
				})
			}
			return results, nil
		},
	}}, Config{})
	resp, err := s.SearchWithOptions(context.Background(), "anything", SearchOptions{Limit: 25})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 25 {
		t.Fatalf("got %d results, want 25", len(resp.Results))
	}
}

func TestSearchWithoutOptionsUsesConfiguredLimit(t *testing.T) {
	s := newSearcherWithSources([]source{fakeSource{
		nameValue: "one",
		searchFn: func(_ context.Context, _ *fetcher, _ string, limit int) ([]sourceResult, error) {
			if limit != defaultLimit {
				t.Fatalf("source received limit %d, want default %d", limit, defaultLimit)
			}
			return []sourceResult{{title: "OK", url: "https://example.com", rank: 1}}, nil
		},
	}}, Config{})
	if _, err := s.Search(context.Background(), "anything"); err != nil {
		t.Fatal(err)
	}
}

// TestFuseResultsNormalisesScore verifies Score is the fusion rank weight
// normalised against the top hit: the best result is 1.0 and later results
// decay relative to it. A hit corroborated by two sources outranks a
// single-source hit at a better position.
func TestFuseResultsNormalisesScore(t *testing.T) {
	outcomes := []sourceOutcome{
		{name: "brave", results: []sourceResult{
			{title: "top", url: "https://a.example/top", rank: 1},
			{title: "mid", url: "https://a.example/mid", rank: 30},
		}},
		{name: "startpage", results: []sourceResult{
			{title: "top", url: "https://a.example/top", rank: 5},
		}},
	}
	results := fuseResults(outcomes, defaultSources(), 10)
	if len(results) != 2 {
		t.Fatalf("results = %#v", results)
	}
	if results[0].URL != "https://a.example/top" || results[0].Score != 1.0 {
		t.Fatalf("top = %#v", results[0])
	}
	// mid raw RRF = 1/90; top raw = 1/61 + 1/65. Normalised ratio:
	want := (1.0 / (rrfK + 30)) / (1.0/(rrfK+1) + 1.0/(rrfK+5))
	if got := results[1].Score; got < want-0.001 || got > want+0.001 {
		t.Fatalf("mid score = %v want ~%v", got, want)
	}
}
