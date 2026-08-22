package searchwire

import (
	"strings"
	"unicode"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
)

// cleanErrorBody converts an HTML error body to compact markdown so it does
// not waste agent tokens with raw HTML tags. Non-HTML bodies are returned
// unchanged. After conversion, runs of 3+ consecutive newlines are collapsed
// to 2 (a common artifact of HTML-to-text conversion) and the result is
// trimmed.
//
// The html-to-markdown converter can return an empty string for pages that
// are almost entirely <script>/<style> with minimal visible content (e.g.
// SvelteKit error pages). In that case we fall back to extractHTMLPage
// (golang.org/x/net/html text extraction) which always produces some text.
// If both produce nothing useful, the original body is returned as-is.
func cleanErrorBody(body string) string {
	if body == "" || !looksLikeHTML([]byte(body)) {
		return body
	}
	md, err := htmltomarkdown.ConvertString(body)
	if err == nil && strings.TrimSpace(md) != "" {
		return collapseNewlines(strings.TrimSpace(md))
	}
	// Fallback: extract visible text via x/net/html. This handles pages
	// where the markdown converter silently produces empty output.
	title, text, _ := extractHTMLPage([]byte(body))
	var parts []string
	if title != "" {
		parts = append(parts, title)
	}
	if text != "" {
		parts = append(parts, text)
	}
	if len(parts) == 0 {
		return body
	}
	return collapseNewlines(strings.TrimSpace(strings.Join(parts, "\n\n")))
}

// collapseNewlines replaces runs of 3+ consecutive newlines with exactly 2
// newlines. This prevents the excessive blank-line spam that HTML-to-text
// conversion often produces.
func collapseNewlines(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	run := 0
	for _, r := range s {
		if r == '\n' {
			run++
			if run <= 2 {
				b.WriteRune(r)
			}
			continue
		}
		if run > 2 {
			// We suppressed extra newlines; flush a single space if the
			// next character is non-whitespace to avoid joining lines.
			if !unicode.IsSpace(r) && b.Len() > 0 {
				last := b.String()[b.Len()-1]
				if last != '\n' && last != ' ' {
					b.WriteByte(' ')
				}
			}
		}
		run = 0
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}
