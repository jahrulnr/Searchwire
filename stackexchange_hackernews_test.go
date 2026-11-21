package searchwire

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestParseStackExchangeFixture(t *testing.T) {
	results, err := parseStackExchangeResults(readFixture(t, "stackexchange-excerpts.json"), "stackoverflow", 10)
	if err != nil {
		t.Fatal(err)
	}
	// Negative-score question dropped; the answer to an already-listed
	// question collapses into it.
	if len(results) != 2 {
		t.Fatalf("results = %#v", results)
	}
	first := results[0]
	if first.title != "How to stop a goroutine on context cancellation & cleanup?" {
		t.Fatalf("title not unescaped: %q", first.title)
	}
	if first.url != "https://stackoverflow.com/questions/11111111" || first.rank != 1 {
		t.Fatalf("first = %#v", first)
	}
	if !strings.Contains(first.snippet, "score 42") || !strings.Contains(first.snippet, "has accepted answer") ||
		!strings.Contains(first.snippet, `"cancelled"`) || strings.Contains(first.snippet, "<span") {
		t.Fatalf("snippet = %q", first.snippet)
	}
	second := results[1]
	if second.url != "https://stackoverflow.com/a/33333333" || !strings.Contains(second.snippet, "accepted") {
		t.Fatalf("second = %#v", second)
	}
}

func TestParseStackExchangeLimitAndGzip(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(readFixture(t, "stackexchange-excerpts.json"))
	zw.Close()
	results, err := parseStackExchangeResults(buf.Bytes(), "stackoverflow", 1)
	if err != nil || len(results) != 1 {
		t.Fatalf("results = %#v err = %v", results, err)
	}
}

func TestParseStackExchangeErrorEnvelope(t *testing.T) {
	_, err := parseStackExchangeResults([]byte(`{"error_id":502,"error_message":"too many requests from this IP","error_name":"throttle_violation"}`), "stackoverflow", 5)
	if err == nil || !strings.Contains(err.Error(), "throttle") && !strings.Contains(err.Error(), "too many") {
		t.Fatalf("err = %v", err)
	}
}

func TestStackExchangeHost(t *testing.T) {
	for site, want := range map[string]string{
		"stackoverflow":          "stackoverflow.com",
		"serverfault":            "serverfault.com",
		"unix":                   "unix.stackexchange.com",
		"math.stackexchange.com": "math.stackexchange.com",
	} {
		if got := stackExchangeHost(site); got != want {
			t.Fatalf("%s → %s, want %s", site, got, want)
		}
	}
}

func TestStackExchangeRequestContract(t *testing.T) {
	var req *http.Request
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		req = r
		return okBody("application/json; charset=utf-8", readFixture(t, "stackexchange-excerpts.json")), nil
	})}
	f := newFetcher(client, defaultUserAgent, defaultMaxResponseBytes)
	if _, err := (stackExchangeSource{apiKey: "se_key", site: "unix"}).search(context.Background(), f, "kill process", 5); err != nil {
		t.Fatal(err)
	}
	q := req.URL.Query()
	if req.URL.Host != "api.stackexchange.com" || req.URL.Path != "/2.3/search/excerpts" ||
		q.Get("q") != "kill process" || q.Get("site") != "unix" || q.Get("sort") != "relevance" ||
		q.Get("pagesize") != "10" || q.Get("key") != "se_key" {
		t.Fatalf("url = %s", req.URL)
	}
}

func TestStackExchangeConfig(t *testing.T) {
	t.Setenv("STACKEXCHANGE_API_KEY", "env_key")
	got := StackExchangeConfig{}.resolved()
	if !got.enabled || got.apiKey != "env_key" || got.site != "stackoverflow" {
		t.Fatalf("resolved = %#v", got)
	}
	disabled := false
	if containsString(sourceNames(sourcesFromConfig(Config{StackExchange: StackExchangeConfig{Enabled: &disabled}})), "stackexchange") {
		t.Fatal("disabled stackexchange still registered")
	}
	if !containsString(New(Config{}).secrets, "env_key") {
		t.Fatal("stackexchange key not registered for redaction")
	}
}

func TestParseHackerNewsFixture(t *testing.T) {
	results, err := parseHackerNewsResults(readFixture(t, "hackernews-search.json"), 10)
	if err != nil {
		t.Fatal(err)
	}
	// Resubmission of the same URL and the hit without objectID drop out.
	if len(results) != 2 {
		t.Fatalf("results = %#v", results)
	}
	first := results[0]
	if first.url != "https://ieftimov.com/post/make-resilient-golang-net-http-servers" ||
		!strings.Contains(first.snippet, "229 points · 40 comments · 2020-01-26") ||
		!strings.Contains(first.snippet, "discussion: https://news.ycombinator.com/item?id=22150395") {
		t.Fatalf("first = %#v", first)
	}
	ask := results[1]
	if ask.url != "https://news.ycombinator.com/item?id=15131877" || strings.Contains(ask.snippet, "discussion:") ||
		!strings.Contains(ask.snippet, `Dave Cheney says "no".`) || ask.rank != 2 {
		t.Fatalf("ask = %#v", ask)
	}
}

func TestHackerNewsRequestContract(t *testing.T) {
	var req *http.Request
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		req = r
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"hits":[]}`)),
		}, nil
	})}
	f := newFetcher(client, defaultUserAgent, defaultMaxResponseBytes)
	if _, err := (hackerNewsSource{}).search(context.Background(), f, "go context", 3); err != nil {
		t.Fatal(err)
	}
	q := req.URL.Query()
	if req.URL.Host != "hn.algolia.com" || q.Get("query") != "go context" || q.Get("tags") != "story" || q.Get("hitsPerPage") != "6" {
		t.Fatalf("url = %s", req.URL)
	}
}

func TestParseHackerNewsMalformed(t *testing.T) {
	if _, err := parseHackerNewsResults([]byte(`{`), 5); err == nil {
		t.Fatal("expected error")
	}
}
