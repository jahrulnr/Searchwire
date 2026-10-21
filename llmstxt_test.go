package searchwire

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

const sampleLLMsTxt = `# Searchwire

> A zero-configuration Go metasearch library.

Fetch one URL or run a metasearch query.

## Docs

- [README](https://example.com/README.md): Installation and usage
- [API reference](https://example.com/api.md)

## Optional

- [Changelog](https://example.com/CHANGELOG.md)
`

func TestParseLLMsTxtFullStructure(t *testing.T) {
	file, err := ParseLLMsTxt(sampleLLMsTxt)
	if err != nil {
		t.Fatal(err)
	}
	if file.Title != "Searchwire" {
		t.Fatalf("title = %q", file.Title)
	}
	if file.Summary != "A zero-configuration Go metasearch library." {
		t.Fatalf("summary = %q", file.Summary)
	}
	if file.Description != "Fetch one URL or run a metasearch query." {
		t.Fatalf("description = %q", file.Description)
	}
	if len(file.Sections) != 2 {
		t.Fatalf("sections = %#v", file.Sections)
	}
	docs := file.Sections[0]
	if docs.Name != "Docs" || len(docs.Links) != 2 {
		t.Fatalf("docs section = %#v", docs)
	}
	if docs.Links[0].Name != "README" ||
		docs.Links[0].URL != "https://example.com/README.md" ||
		docs.Links[0].Notes != "Installation and usage" {
		t.Fatalf("link 0 = %#v", docs.Links[0])
	}
	if docs.Links[1].Notes != "" {
		t.Fatalf("link 1 notes = %q", docs.Links[1].Notes)
	}
	opt := file.Sections[1]
	if opt.Name != "Optional" || len(opt.Links) != 1 || opt.Links[0].URL != "https://example.com/CHANGELOG.md" {
		t.Fatalf("optional section = %#v", opt)
	}
}

func TestParseLLMsTxtMinimalTitleOnly(t *testing.T) {
	file, err := ParseLLMsTxt("# Just a title\n")
	if err != nil {
		t.Fatal(err)
	}
	if file.Title != "Just a title" || file.Summary != "" || len(file.Sections) != 0 {
		t.Fatalf("file = %#v", file)
	}
}

func TestParseLLMsTxtBOMAndCRLF(t *testing.T) {
	file, err := ParseLLMsTxt("\xef\xbb\xbf# BOM Site\r\n\r\n> Summary line.\r\n")
	if err != nil {
		t.Fatal(err)
	}
	if file.Title != "BOM Site" || file.Summary != "Summary line." {
		t.Fatalf("file = %#v", file)
	}
}

func TestParseLLMsTxtMissingH1IsInvalid(t *testing.T) {
	_, err := ParseLLMsTxt("> summary only\n\n## Section\n\n- [x](https://a)\n")
	if !errors.Is(err, ErrInvalidLLMsTxt) {
		t.Fatalf("err = %v", err)
	}
}

func TestParseLLMsTxtIgnoresNonListLinesInsideSection(t *testing.T) {
	file, err := ParseLLMsTxt("# T\n\n## S\n\nSome prose line.\n\n- [a](https://a)\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Sections) != 1 || len(file.Sections[0].Links) != 1 {
		t.Fatalf("sections = %#v", file.Sections)
	}
	if strings.Contains(file.Description, "prose") {
		t.Fatalf("section prose leaked into description: %q", file.Description)
	}
}

func llmsTestSearcher(statusFor func(url string) (int, string)) *Searcher {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		status, body := statusFor(r.URL.String())
		contentType := "text/markdown"
		if status == http.StatusNotFound {
			contentType = "text/plain"
			body = "not found"
		}
		return &http.Response{
			StatusCode: status,
			Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
			Header:     http.Header{"Content-Type": []string{contentType}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    r,
		}, nil
	})}
	return New(Config{HTTPClient: client})
}

func TestFetchLLMsTxtPrefersMostSpecificFile(t *testing.T) {
	var requested []string
	s := llmsTestSearcher(func(url string) (int, string) {
		requested = append(requested, url)
		if url == "https://example.com/docs/llms.txt" {
			return 200, "# Docs Coverage\n"
		}
		return 200, "# Root Fallback\n"
	})
	res, err := s.FetchLLMsTxt(context.Background(), "https://example.com/docs/page.html")
	if err != nil {
		t.Fatal(err)
	}
	if res.FinalURL != "https://example.com/docs/llms.txt" {
		t.Fatalf("final = %q", res.FinalURL)
	}
	if res.LLMsTxt.Title != "Docs Coverage" {
		t.Fatalf("title = %q", res.LLMsTxt.Title)
	}
	if len(requested) != 1 || requested[0] != "https://example.com/docs/llms.txt" {
		t.Fatalf("requests = %v", requested)
	}
}

func TestFetchLLMsTxtFallsBackToRootOn404(t *testing.T) {
	var requested []string
	s := llmsTestSearcher(func(url string) (int, string) {
		requested = append(requested, url)
		if url == "https://example.com/llms.txt" {
			return 200, "# Root File\n\n## Docs\n\n- [g](https://example.com/g.md)\n"
		}
		return 404, ""
	})
	res, err := s.FetchLLMsTxt(context.Background(), "https://example.com/deep/nested/page.html")
	if err != nil {
		t.Fatal(err)
	}
	if res.URL != "https://example.com/llms.txt" || res.StatusCode != 200 {
		t.Fatalf("result = %#v", res)
	}
	if len(res.LLMsTxt.Sections) != 1 || res.LLMsTxt.Sections[0].Links[0].Name != "g" {
		t.Fatalf("parsed = %#v", res.LLMsTxt)
	}
	if len(requested) != 2 {
		t.Fatalf("requests = %v", requested)
	}
}

func TestFetchLLMsTxtNotFoundSentinelWhenAll404(t *testing.T) {
	s := llmsTestSearcher(func(string) (int, string) { return 404, "" })
	_, err := s.FetchLLMsTxt(context.Background(), "https://example.com/page.html")
	if !errors.Is(err, ErrLLMsTxtNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestFetchLLMsTxtSoft404HTMLFallsThroughToValidRoot(t *testing.T) {
	// SPA-style soft 404: sub-path returns HTML with 200; the parser must
	// reject it (no H1) and the root probe must still win.
	s := llmsTestSearcher(func(url string) (int, string) {
		if url == "https://example.com/docs/llms.txt" {
			return 200, "<!doctype html><html><head><title>SPA</title></head><body>fallback</body></html>"
		}
		return 200, "# Real Root\n"
	})
	res, err := s.FetchLLMsTxt(context.Background(), "https://example.com/docs/page.html")
	if err != nil {
		t.Fatal(err)
	}
	if res.LLMsTxt.Title != "Real Root" {
		t.Fatalf("title = %q", res.LLMsTxt.Title)
	}
}

func TestFetchLLMsTxtInvalidURLRejectedBeforeProbing(t *testing.T) {
	s := New(Config{})
	if _, err := s.FetchLLMsTxt(context.Background(), "ftp://example.com/x"); !errors.Is(err, ErrUnsupportedScheme) {
		t.Fatalf("err = %v", err)
	}
	var s2 *Searcher
	if _, err := s2.FetchLLMsTxt(context.Background(), "https://example.com"); !errors.Is(err, ErrNilSearcher) {
		t.Fatalf("err = %v", err)
	}
}

func TestFetchSurfacesLLMsTxtDiscoveryHintsFromHTMLLinks(t *testing.T) {
	body := `<!doctype html><html><head><title>Hints</title>
<link rel="describedby" href="/docs/llms.txt">
<link rel="alternate" type="text/markdown" href="page.html.md">
</head><body><p>content</p></body></html>`
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/html"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    r,
		}, nil
	})}
	result, err := New(Config{HTTPClient: client}).Fetch(context.Background(), "https://example.com/docs/page.html")
	if err != nil {
		t.Fatal(err)
	}
	if result.DescribedBy != "https://example.com/docs/llms.txt" {
		t.Fatalf("DescribedBy = %q", result.DescribedBy)
	}
	if result.MarkdownAlt != "https://example.com/docs/page.html.md" {
		t.Fatalf("MarkdownAlt = %q", result.MarkdownAlt)
	}
	// The fixture serves the same HTML for the alternate URL — a soft-404 —
	// so the alternate must be detected but not used as the text.
	if result.MarkdownAltUsed {
		t.Fatal("HTML-served alternate should not be used")
	}
}

func TestFetchSurfacesHintsFromHTTPLinkHeader(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header: http.Header{
				"Content-Type": []string{"text/plain"},
				"Link":         []string{`</docs/llms.txt>; rel="describedby", </page.md>; rel="alternate"; type="text/markdown"`},
			},
			Body:    io.NopCloser(strings.NewReader("body")),
			Request: r,
		}, nil
	})}
	result, err := New(Config{HTTPClient: client}).Fetch(context.Background(), "https://example.com/docs/page.html")
	if err != nil {
		t.Fatal(err)
	}
	if result.DescribedBy != "https://example.com/docs/llms.txt" {
		t.Fatalf("DescribedBy = %q", result.DescribedBy)
	}
	// "</page.md>" is root-relative (leading slash), so it resolves against
	// the origin, not the /docs/ directory.
	if result.MarkdownAlt != "https://example.com/page.md" {
		t.Fatalf("MarkdownAlt = %q", result.MarkdownAlt)
	}
}

func TestFetchHintsEmptyWhenSiteDoesNotAdvertise(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/plain"}},
			Body:       io.NopCloser(strings.NewReader("plain")),
			Request:    r,
		}, nil
	})}
	result, err := New(Config{HTTPClient: client}).Fetch(context.Background(), "https://example.com/x")
	if err != nil {
		t.Fatal(err)
	}
	if result.DescribedBy != "" || result.MarkdownAlt != "" {
		t.Fatalf("hints = %q / %q", result.DescribedBy, result.MarkdownAlt)
	}
}

func TestFindLinkHeaderParsesQuotedAndBareParams(t *testing.T) {
	header := `<https://a/llms.txt>; rel="describedby", <b.md>; rel=alternate; type=text/markdown`
	if got := findLinkHeader([]string{header}, "describedby", ""); got != "https://a/llms.txt" {
		t.Fatalf("describedby = %q", got)
	}
	if got := findLinkHeader([]string{header}, "alternate", "text/markdown"); got != "b.md" {
		t.Fatalf("markdown alt = %q", got)
	}
	if got := findLinkHeader([]string{header}, "nofollow", ""); got != "" {
		t.Fatalf("unexpected match %q", got)
	}
}
