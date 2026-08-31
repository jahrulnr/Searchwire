package searchwire

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// TestSearchWithOptionsSourcesFilter verifies that a per-call source
// restriction limits the fan-out to the named registered sources.
func TestSearchWithOptionsSourcesFilter(t *testing.T) {
	var mu sync.Mutex
	called := []string{}
	s := newSearcherWithSources([]source{
		fakeSource{nameValue: "a", searchFn: func(_ context.Context, _ *fetcher, _ string, _ int) ([]sourceResult, error) {
			mu.Lock()
			called = append(called, "a")
			mu.Unlock()
			return okSource("https://example.com/a")(nil, nil, "", 0)
		}},
		fakeSource{nameValue: "b", searchFn: func(_ context.Context, _ *fetcher, _ string, _ int) ([]sourceResult, error) {
			mu.Lock()
			called = append(called, "b")
			mu.Unlock()
			return okSource("https://example.com/b")(nil, nil, "", 0)
		}},
		fakeSource{nameValue: "c", searchFn: func(_ context.Context, _ *fetcher, _ string, _ int) ([]sourceResult, error) {
			mu.Lock()
			called = append(called, "c")
			mu.Unlock()
			return okSource("https://example.com/c")(nil, nil, "", 0)
		}},
	}, Config{})

	resp, err := s.SearchWithOptions(context.Background(), "test", SearchOptions{Sources: []string{"b"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 || resp.Results[0].URL != "https://example.com/b" {
		t.Fatalf("results = %#v", resp.Results)
	}
	if len(called) != 1 || called[0] != "b" {
		t.Fatalf("called = %#v", called)
	}
}

// TestSearchWithOptionsSourcesFilterPartial verifies unknown names are
// ignored while the known ones still run (no hard failure on typos).
func TestSearchWithOptionsSourcesFilterPartial(t *testing.T) {
	s := newSearcherWithSources([]source{
		fakeSource{nameValue: "a", searchFn: okSource("https://example.com/a")},
		fakeSource{nameValue: "b", searchFn: okSource("https://example.com/b")},
	}, Config{})
	resp, err := s.SearchWithOptions(context.Background(), "test", SearchOptions{Sources: []string{"a", "nope"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 || resp.Results[0].URL != "https://example.com/a" {
		t.Fatalf("results = %#v", resp.Results)
	}
}

// TestSearchWithOptionsSourcesFilterOnlyUnknown verifies a restriction to
// only unknown names behaves like a fan-out with no sources: a SearchError.
func TestSearchWithOptionsSourcesFilterOnlyUnknown(t *testing.T) {
	s := newSearcherWithSources([]source{
		fakeSource{nameValue: "a", searchFn: okSource("https://example.com/a")},
	}, Config{})
	_, err := s.SearchWithOptions(context.Background(), "test", SearchOptions{Sources: []string{"nope"}})
	var searchErr *SearchError
	if !errors.As(err, &searchErr) {
		t.Fatalf("err = %v", err)
	}
}

// TestSearchWithOptionsSourcesFilterCaseInsensitive verifies name matching
// trims whitespace and ignores case.
func TestSearchWithOptionsSourcesFilterCaseInsensitive(t *testing.T) {
	s := newSearcherWithSources([]source{
		fakeSource{nameValue: "wikipedia", searchFn: okSource("https://en.wikipedia.org/wiki/Test")},
		fakeSource{nameValue: "startpage", searchFn: okSource("https://example.com/start")},
	}, Config{})
	resp, err := s.SearchWithOptions(context.Background(), "test", SearchOptions{Sources: []string{"  WIKIPEDIA "}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 || !strings.Contains(resp.Results[0].URL, "wikipedia") {
		t.Fatalf("results = %#v", resp.Results)
	}
}

// TestSearcherSources lists registered source names in configuration order.
func TestSearcherSources(t *testing.T) {
	s := newSearcherWithSources([]source{
		fakeSource{nameValue: "brave"},
		fakeSource{nameValue: "serper"},
		fakeSource{nameValue: "tavily"},
	}, Config{})
	names := s.Sources()
	if len(names) != 3 || names[0] != "brave" || names[1] != "serper" || names[2] != "tavily" {
		t.Fatalf("names = %#v", names)
	}
	if s := (*Searcher)(nil); s.Sources() != nil {
		t.Fatalf("nil searcher should return nil sources")
	}
}
