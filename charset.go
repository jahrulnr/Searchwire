package searchwire

import (
	"regexp"
	"strings"

	"golang.org/x/text/encoding/htmlindex"
)

var metaCharsetRe = regexp.MustCompile(`(?i)<meta[^>]*charset=["']?([a-zA-Z0-9_\-:.]+)`)

// decodeBody decodes body from the named charset to UTF-8. Empty, utf-8, or
// ascii charsets return body unchanged. Unknown charsets return body
// unchanged (best-effort, never fail the fetch). Partial decode output is
// preferred over the original when a multi-byte decoder errors mid-stream.
func decodeBody(body []byte, charset string) []byte {
	charset = strings.TrimSpace(strings.ToLower(charset))
	switch charset {
	case "", "utf-8", "utf8", "us-ascii", "ascii":
		return body
	}
	enc, err := htmlindex.Get(charset)
	if err != nil {
		return body
	}
	decoded, err := enc.NewDecoder().Bytes(body)
	if len(decoded) == 0 {
		return body
	}
	return decoded
}

// sniffHTMLCharset scans the first 1KB of an HTML body for a <meta charset>
// declaration (either <meta charset="..."> or
// <meta http-equiv content-type content="...; charset=...">). Returns "" if
// none found. Used when the Content-Type header omits a charset.
func sniffHTMLCharset(body []byte) string {
	sample := body
	if len(sample) > 1024 {
		sample = sample[:1024]
	}
	if m := metaCharsetRe.FindSubmatch(sample); len(m) >= 2 {
		return string(m[1])
	}
	return ""
}
