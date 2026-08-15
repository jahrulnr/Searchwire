package searchwire

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestFetchHTMLSkipsNavFooterAside: nav/footer/aside/form text is boilerplate
// and must not leak into the readable text, while main content is preserved.
func TestFetchHTMLSkipsNavFooterAside(t *testing.T) {
	body := `<!doctype html><html><head><title>P</title></head><body>
<nav>Home About Contact</nav>
<header>Site Banner</header>
<main><h1>Real Article</h1><p>Important body content.</p></main>
<aside>Related links sidebar</aside>
<footer>Copyright nobody</footer>
<form><label>Email</label><input></form>
</body></html>`
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200, Status: "200 OK",
			Header:  http.Header{"Content-Type": []string{"text/html"}},
			Body:    io.NopCloser(strings.NewReader(body)),
			Request: r,
		}, nil
	})}
	result, err := New(Config{HTTPClient: client}).Fetch(context.Background(), "https://example.com/p")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text, "Real Article") || !strings.Contains(result.Text, "Important body content") {
		t.Fatalf("main content missing: %q", result.Text)
	}
	for _, boiler := range []string{"Home About Contact", "Related links sidebar", "Copyright nobody"} {
		if strings.Contains(result.Text, boiler) {
			t.Fatalf("boilerplate leaked: %q in %q", boiler, result.Text)
		}
	}
}

// TestFetchHTMLPreservesPreCode: <pre>/<code> whitespace and newlines must be
// preserved so code samples stay readable.
func TestFetchHTMLPreservesPreCode(t *testing.T) {
	body := `<!doctype html><html><body><pre>func main() {
	fmt.Println("hi")
}</pre></body></html>`
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200, Status: "200 OK",
			Header:  http.Header{"Content-Type": []string{"text/html"}},
			Body:    io.NopCloser(strings.NewReader(body)),
			Request: r,
		}, nil
	})}
	result, err := New(Config{HTTPClient: client}).Fetch(context.Background(), "https://example.com/code")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text, `fmt.Println("hi")`) {
		t.Fatalf("pre code content lost: %q", result.Text)
	}
	if !strings.Contains(result.Text, "func main() {\n") {
		t.Fatalf("pre newlines collapsed: %q", result.Text)
	}
}

// TestFetchHTMLLinksCollected: anchor hrefs with safe URLs are surfaced in
// result.Links so the agent can follow them without re-parsing.
func TestFetchHTMLLinksCollected(t *testing.T) {
	body := `<!doctype html><html><body>
<a href="https://example.com/a">A</a>
<a href="/relative">Rel</a>
<a href="javascript:evil()">bad</a>
<a href="https://other.com/b">B</a>
</body></html>`
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200, Status: "200 OK",
			Header:  http.Header{"Content-Type": []string{"text/html"}},
			Body:    io.NopCloser(strings.NewReader(body)),
			Request: r,
		}, nil
	})}
	result, err := New(Config{HTTPClient: client}).Fetch(context.Background(), "https://example.com/links")
	if err != nil {
		t.Fatal(err)
	}
	hrefs := map[string]bool{}
	for _, l := range result.Links {
		hrefs[l.Href] = true
	}
	if !hrefs["https://example.com/a"] || !hrefs["https://other.com/b"] {
		t.Fatalf("safe links missing: %+v", result.Links)
	}
	if hrefs["javascript:evil()"] {
		t.Fatalf("unsafe link collected: %+v", result.Links)
	}
}

// TestFetchHTMLTitleOGFallback: when <title> is empty, og:title meta is used.
func TestFetchHTMLTitleOGFallback(t *testing.T) {
	body := `<!doctype html><html><head><meta property="og:title" content="OG Title"></head><body>x</body></html>`
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200, Status: "200 OK",
			Header:  http.Header{"Content-Type": []string{"text/html"}},
			Body:    io.NopCloser(strings.NewReader(body)),
			Request: r,
		}, nil
	})}
	result, err := New(Config{HTTPClient: client}).Fetch(context.Background(), "https://example.com/og")
	if err != nil {
		t.Fatal(err)
	}
	if result.Title != "OG Title" {
		t.Fatalf("expected og:title fallback, got %q", result.Title)
	}
}

// TestFetchHTMLTitleH1Fallback: when no <title> and no og:title, first <h1>.
func TestFetchHTMLTitleH1Fallback(t *testing.T) {
	body := `<!doctype html><html><body><h1>Heading One</h1><p>body</p></body></html>`
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200, Status: "200 OK",
			Header:  http.Header{"Content-Type": []string{"text/html"}},
			Body:    io.NopCloser(strings.NewReader(body)),
			Request: r,
		}, nil
	})}
	result, err := New(Config{HTTPClient: client}).Fetch(context.Background(), "https://example.com/h1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Title != "Heading One" {
		t.Fatalf("expected h1 fallback, got %q", result.Title)
	}
}
