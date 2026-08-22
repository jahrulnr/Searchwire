package searchwire

import (
	"strings"
	"testing"
)

// TestCleanErrorBodyConvertsHTMLToMarkdown: an HTML error body (like a
// SvelteKit 429 page) must be converted to compact markdown without raw
// HTML tags.
func TestCleanErrorBodyConvertsHTMLToMarkdown(t *testing.T) {
	html := `<!doctype html>
<html lang="en-us">
  <head>
    <meta charset="utf-8" />
    <title>429 Too Many Requests</title>
  </head>
  <body>
    <h1>429 Too Many Requests</h1>
    <p>You have been rate limited. Please try again in 30 seconds.</p>
  </body>
</html>`

	cleaned := cleanErrorBody(html)
	if strings.Contains(cleaned, "<html") || strings.Contains(cleaned, "<body") || strings.Contains(cleaned, "<head") {
		t.Errorf("cleaned body still contains HTML tags: %s", cleaned)
	}
	if !strings.Contains(cleaned, "429 Too Many Requests") {
		t.Errorf("cleaned body lost the error message: %s", cleaned)
	}
	if !strings.Contains(cleaned, "rate limited") {
		t.Errorf("cleaned body lost the description: %s", cleaned)
	}
}

// TestCleanErrorBodyNonHTMLUnchanged: a plain-text or JSON body must be
// returned unchanged.
func TestCleanErrorBodyNonHTMLUnchanged(t *testing.T) {
	cases := []string{
		"slow down",
		`{"error":"rate_limited"}`,
		"",
		"just plain text",
	}
	for _, body := range cases {
		got := cleanErrorBody(body)
		if got != body {
			t.Errorf("non-HTML body should be unchanged: got %q, want %q", got, body)
		}
	}
}

// TestCleanErrorBodyCollapsesExcessiveNewlines: HTML-to-text conversion
// often produces runs of 3+ newlines; these must be collapsed to at most 2.
func TestCleanErrorBodyCollapsesExcessiveNewlines(t *testing.T) {
	html := `<!doctype html>
<html><body>
<p>Line 1</p>


<p>Line 2</p>



<p>Line 3</p>
</body></html>`

	cleaned := cleanErrorBody(html)
	// No run of 3+ newlines should remain.
	if strings.Contains(cleaned, "\n\n\n") {
		t.Errorf("cleaned body has 3+ consecutive newlines: %q", cleaned)
	}
}

// TestCollapseNewlines: unit test for the newline collapser.
func TestCollapseNewlines(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"a\nb", "a\nb"},
		{"a\n\nb", "a\n\nb"},
		{"a\n\n\nb", "a\n\nb"},
		{"a\n\n\n\n\nb", "a\n\nb"},
		{"hello", "hello"},
		{"", ""},
		{"\n\n\n", ""},
		{"a\n\n\n\n\n", "a"},
	}
	for _, c := range cases {
		got := collapseNewlines(c.input)
		if got != c.want {
			t.Errorf("collapseNewlines(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

// TestLooksLikeHTML: heuristic detection of HTML bodies (existing function
// in fetch.go, verified here for the error-cleaning path).
func TestLooksLikeHTML(t *testing.T) {
	htmlCases := []string{
		"<!doctype html>",
		"<html>",
		"<HTML>",
		"  <html lang=\"en\">",
		"<head><title>x</title></head>",
		"<!DOCTYPE html>\n<html>",
	}
	for _, body := range htmlCases {
		if !looksLikeHTML([]byte(body)) {
			t.Errorf("looksLikeHTML(%q) = false, want true", body)
		}
	}
	nonHTMLCases := []string{
		"slow down",
		`{"error":"rate_limited"}`,
		"just plain text",
		"",
		"<not really html>",
	}
	for _, body := range nonHTMLCases {
		if looksLikeHTML([]byte(body)) {
			t.Errorf("looksLikeHTML(%q) = true, want false", body)
		}
	}
}

// TestHTTPErrorErrorCleansHTML: the Error() method must produce a compact
// string from an HTML body, not dump raw HTML.
func TestHTTPErrorErrorCleansHTML(t *testing.T) {
	e := &HTTPError{
		StatusCode: 429,
		Status:     "429 Too Many Requests",
		Body:       "<!doctype html><html><body><h1>429 Too Many Requests</h1><p>Rate limited.</p></body></html>",
	}
	msg := e.Error()
	if strings.Contains(msg, "<html") || strings.Contains(msg, "<body") || strings.Contains(msg, "<h1") {
		t.Errorf("Error() contains raw HTML: %s", msg)
	}
	if !strings.Contains(msg, "429 Too Many Requests") {
		t.Errorf("Error() lost the status: %s", msg)
	}
}

// TestHTTPErrorErrorPreservesPlainText: non-HTML bodies must be unchanged.
func TestHTTPErrorErrorPreservesPlainText(t *testing.T) {
	e := &HTTPError{
		StatusCode: 429,
		Status:     "429 Too Many Requests",
		Body:       "slow down",
	}
	msg := e.Error()
	want := "searchwire: source returned 429 Too Many Requests: slow down"
	if msg != want {
		t.Errorf("Error() = %q, want %q", msg, want)
	}
}
