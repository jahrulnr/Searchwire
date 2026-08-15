package searchwire

import (
	"encoding/json"
	"strings"
)

// isJSONMediaType reports whether mediaType is JSON (exact or +json vendor).
func isJSONMediaType(mediaType string) bool {
	if mediaType == "application/json" {
		return true
	}
	return strings.HasSuffix(mediaType, "+json")
}

// isXMLMediaType reports whether mediaType is XML-ish (xml, rss, atom, +xml).
func isXMLMediaType(mediaType string) bool {
	switch mediaType {
	case "application/xml", "text/xml", "application/rss+xml", "application/atom+xml":
		return true
	}
	return strings.HasSuffix(mediaType, "+xml")
}

// isFetchAllowedMediaType is the content-type allowlist for Fetch. HTML,
// plain text, and structured text formats (JSON, XML, RSS, Atom, Markdown,
// CSV) are accepted. Vendor +json/+xml suffixes are accepted too.
func isFetchAllowedMediaType(mt string) bool {
	switch mt {
	case "text/html", "application/xhtml+xml", "text/plain",
		"application/json", "application/xml", "text/xml",
		"application/rss+xml", "application/atom+xml",
		"text/markdown", "text/csv":
		return true
	}
	if strings.HasPrefix(mt, "text/") {
		return true
	}
	if strings.HasSuffix(mt, "+json") || strings.HasSuffix(mt, "+xml") {
		return true
	}
	return false
}

// prettyJSON re-serializes a JSON body with 2-space indentation so the agent
// can read structure. Invalid JSON returns the raw body unchanged instead of
// failing the fetch.
func prettyJSON(body []byte) string {
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return string(body)
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return string(body)
	}
	return string(out)
}

// stripXMLTags removes XML/RSS/Atom markup and returns readable text with
// newlines preserved between elements. It is a pragmatic tag stripper, not a
// validating parser; mixed namespaces and CDATA are handled best-effort.
func stripXMLTags(body []byte) string {
	var b strings.Builder
	inTag := false
	for _, r := range string(body) {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
			b.WriteByte('\n')
		case !inTag:
			b.WriteRune(r)
		}
	}
	return collapseInlineWhitespace(b.String())
}

// collapseInlineWhitespace collapses runs of spaces/tabs within each line and
// trims trailing whitespace per line, but preserves newlines. This keeps
// structure for plain text, markdown, CSV, and stripped XML where
// collapseWhitespace would flatten everything into one line.
func collapseInlineWhitespace(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.Join(strings.Fields(line), " ")
	}
	return strings.Join(lines, "\n")
}
