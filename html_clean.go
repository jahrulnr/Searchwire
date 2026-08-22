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
// trimmed. If conversion fails, the original body is returned as a fallback.
func cleanErrorBody(body string) string {
	if body == "" || !looksLikeHTML([]byte(body)) {
		return body
	}
	md, err := htmltomarkdown.ConvertString(body)
	if err != nil || md == "" {
		return body
	}
	return collapseNewlines(strings.TrimSpace(md))
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
