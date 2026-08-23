package searchwire

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// FetchResult is the readable text extracted from one fetched page.
type FetchResult struct {
	URL         string
	FinalURL    string
	StatusCode  int
	ContentType string
	Title       string
	Text        string
	Links       []Link
	// Headers carries selected response headers useful to agents:
	// ETag, Last-Modified, X-RateLimit-Remaining, X-RateLimit-Reset,
	// Retry-After (on 429/503). Empty when absent.
	Headers map[string]string
	// Redirects is the number of redirects followed (0 = direct hit).
	Redirects int
	Truncated bool
	Bytes     int
	// DescribedBy is the absolute URL of the llms.txt file covering this
	// page, discovered via <link rel="describedby"> or an equivalent HTTP
	// Link header (llmstxt.org v2). Empty when the site does not advertise
	// one.
	DescribedBy string
	// MarkdownAlt is the absolute URL of a markdown alternate version of
	// this page (<link rel="alternate" type="text/markdown"> or the HTTP
	// Link header equivalent). Empty when absent.
	MarkdownAlt string
}

// Link is one anchor collected from an HTML page (text + href). Only safe
// http/https hrefs are collected; javascript:, data:, and protocol-relative
// URLs are skipped.
type Link struct {
	Text string
	Href string
}

// Fetch retrieves one http(s) URL and returns readable text.
// HTML responses are stripped to title + visible text. Plain text is returned
// as-is. Extracted text — not raw HTML — is capped at Config.MaxResponseBytes;
// Truncated reports when the returned text was cut to fit. Raw HTML downloads
// are additionally bounded by an internal hard ceiling (defaultMaxRawResponseBytes).
// This is a local agent helper, not a hardened proxy.
func (s *Searcher) Fetch(ctx context.Context, rawURL string) (*FetchResult, error) {
	return s.FetchWithOptions(ctx, rawURL, FetchOptions{})
}

// FetchWithLimit is Fetch with an optional per-call byte budget for the
// extracted readable text. When maxBytes <= 0, Config.MaxResponseBytes is used.
// The budget applies to the returned text, not the raw HTML download, so HTML
// overhead (markup, scripts, styles) does not consume the caller's budget.
func (s *Searcher) FetchWithLimit(ctx context.Context, rawURL string, maxBytes int64) (*FetchResult, error) {
	return s.FetchWithOptions(ctx, rawURL, FetchOptions{MaxBytes: maxBytes})
}

// FetchOptions controls one fetch call: byte budget, conditional GET headers,
// and a Range request. Zero values mean "do not send".
type FetchOptions struct {
	// MaxBytes caps the response body. When <= 0, Config.MaxResponseBytes is
	// used (or the library default).
	MaxBytes int64
	// IfNoneMatch is sent as If-None-Match for conditional GET. A 304
	// response is returned as a FetchResult with StatusCode 304 (not an
	// error).
	IfNoneMatch string
	// IfModifiedSince is sent as If-Modified-Since for conditional GET.
	IfModifiedSince string
	// Range is sent as the Range header (e.g. "bytes=10-20"). A 206 response
	// is returned as a normal FetchResult with StatusCode 206.
	Range string
}

// FetchWithOptions is Fetch with conditional-GET / Range / byte-budget
// controls.
func (s *Searcher) FetchWithOptions(ctx context.Context, rawURL string, opts FetchOptions) (*FetchResult, error) {
	if s == nil {
		return nil, ErrNilSearcher
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	canonical, err := validateFetchURL(rawURL)
	if err != nil {
		return nil, err
	}

	// Budget for the extracted text result (what the caller wants).
	textLimit := s.maxResponseSize
	if opts.MaxBytes > 0 && (textLimit <= 0 || opts.MaxBytes < textLimit) {
		textLimit = opts.MaxBytes
	}
	if textLimit <= 0 {
		textLimit = defaultMaxResponseBytes
	}

	// Hard ceiling for the raw HTML/plain-text download (safety: memory + bandwidth).
	rawLimit := s.maxRawResponseSize
	if rawLimit <= 0 {
		rawLimit = defaultMaxRawResponseBytes
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, canonical, nil)
	if err != nil {
		return nil, fmt.Errorf("searchwire: create request: %w", err)
	}
	req.Header.Set("Accept", "text/html, application/xhtml+xml, text/plain, text/markdown, text/csv, application/json, application/xml, text/xml, application/rss+xml, application/atom+xml, text/*;q=0.8, */*;q=0.1")
	req.Header.Set("User-Agent", s.userAgent)
	if opts.IfNoneMatch != "" {
		req.Header.Set("If-None-Match", opts.IfNoneMatch)
	}
	if opts.IfModifiedSince != "" {
		req.Header.Set("If-Modified-Since", opts.IfModifiedSince)
	}
	if opts.Range != "" {
		req.Header.Set("Range", opts.Range)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Read the full body up to the hard raw ceiling. +1 lets us detect and
	// report oversize responses cleanly rather than silently mis-parsing.
	body, err := io.ReadAll(io.LimitReader(resp.Body, rawLimit+1))
	if err != nil {
		return nil, fmt.Errorf("searchwire: read response: %w", err)
	}
	if int64(len(body)) > rawLimit {
		return nil, fmt.Errorf("%w: raw response exceeds hard ceiling (%d bytes)", ErrResponseTooLarge, rawLimit)
	}

	contentType := resp.Header.Get("Content-Type")
	mediaType, charset := parseMediaType(contentType)
	if err := validateFetchContentType(mediaType); err != nil {
		return nil, err
	}

	// 304 Not Modified and 206 Partial Content are success-family for
	// conditional GET / Range; treat them as 2xx for the body path.
	isConditionalOK := resp.StatusCode == http.StatusNotModified || resp.StatusCode == http.StatusPartialContent
	if (!isConditionalOK) && (resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices) {
		return nil, newHTTPError(resp, body)
	}

	finalURL := canonical
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}

	// Decode non-UTF-8 bodies. For HTML without a header charset, sniff a
	// <meta charset> declaration before decoding so the parser sees UTF-8.
	if charset == "" && (isHTMLMediaType(mediaType) || (mediaType == "" && looksLikeHTML(body))) {
		charset = sniffHTMLCharset(body)
	}
	body = decodeBody(body, charset)

	// Extract the readable text from the complete body, THEN apply the caller's
	// budget to the extracted text. This keeps small budgets useful: a 5 KB
	// budget now yields 5 KB of real content instead of a fragment of <head>.
	title, text, links, hints := extractFetchText(mediaType, body)
	text, truncated := truncateRunes(text, textLimit)
	if truncated {
		// Reserve space for the marker within the budget so the total text
		// length stays at textLimit. Skip the marker when the budget is too
		// small to hold both content and the marker.
		const marker = "\n[truncated]"
		if textLimit > int64(len(marker)) {
			text, _ = truncateRunes(text, textLimit-int64(len(marker)))
			text = strings.TrimRight(text, "\n") + marker
		}
	}
	return &FetchResult{
		URL:         canonical,
		FinalURL:    finalURL,
		StatusCode:  resp.StatusCode,
		ContentType: contentType,
		Title:       title,
		Text:        text,
		Links:       links,
		Headers:     selectedHeaders(resp),
		Redirects:   redirectCount(resp),
		Truncated:   truncated,
		Bytes:       len(text),
		DescribedBy: resolveRef(finalURL, firstNonEmpty(hints.describedBy,
			findLinkHeader(resp.Header["Link"], "describedby", ""))),
		MarkdownAlt: resolveRef(finalURL, firstNonEmpty(hints.markdownAlt,
			findLinkHeader(resp.Header["Link"], "alternate", "text/markdown"))),
	}, nil
}

// newHTTPError builds an HTTPError from a non-2xx response, parsing
// Retry-After (seconds) and a JSON error envelope when present.
func newHTTPError(resp *http.Response, body []byte) *HTTPError {
	e := &HTTPError{
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
		Body:       strings.TrimSpace(string(body)),
	}
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if n, err := parseRetryAfter(ra); err == nil {
			e.RetryAfter = n
		}
	}
	if mt, _ := parseMediaType(resp.Header.Get("Content-Type")); isJSONMediaType(mt) {
		e.ErrorBody = parseJSONErrorEnvelope(body)
	}
	return e
}

// selectedHeaders copies a small set of agent-useful headers into a map.
// Lookup is case-insensitive against the canonical header names so it works
// whether the server (or test transport) used canonical or literal casing.
func selectedHeaders(resp *http.Response) map[string]string {
	wanted := []string{"ETag", "Last-Modified", "X-RateLimit-Remaining", "X-RateLimit-Reset", "Retry-After", "Content-Range"}
	out := make(map[string]string, len(wanted))
	for _, h := range wanted {
		if v := headerGetCI(resp.Header, h); v != "" {
			out[h] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// headerGetCI is a case-insensitive Header.Get. http.Header.Get already
// canonicalizes the lookup key, but a transport that populates the map with
// literal (non-canonical) keys defeats that. This iterates so both forms hit.
func headerGetCI(h http.Header, name string) string {
	for k, vs := range h {
		if strings.EqualFold(k, name) && len(vs) > 0 {
			return vs[0]
		}
	}
	return ""
}

// redirectCount returns the number of redirects followed to reach resp by
// walking resp.Request.Response chain (set by net/http when CheckRedirect
// follows). Returns 0 when there was no redirect.
func redirectCount(resp *http.Response) int {
	if resp == nil || resp.Request == nil {
		return 0
	}
	count := 0
	prev := resp.Request.Response
	for prev != nil && prev.StatusCode >= 300 && prev.StatusCode < 400 {
		count++
		if prev.Request == nil {
			break
		}
		next := prev.Request.Response
		if next == prev {
			break
		}
		prev = next
	}
	return count
}

// truncateRunes cuts value to at most limit runes (counting user-perceived
// characters rather than bytes keeps CJK/emoji natural) on a rune boundary.
// The limit is interpreted as bytes for the common ASCII-heavy web case:
// because rune count <= byte count, truncating by runes never exceeds the
// byte budget for byte-based callers while avoiding mid-rune corruption.
func truncateRunes(value string, limit int64) (string, bool) {
	if limit <= 0 || int64(len(value)) <= limit {
		return value, false
	}
	// Fast path: the byte length exceeds the limit; cut at a rune boundary at
	// or before limit bytes so we never split a UTF-8 sequence.
	if !utf8.ValidString(value) {
		return string([]rune(value)[:limit]), true
	}
	cut := 0
	for i := range value {
		if int64(i) > limit {
			break
		}
		cut = i
	}
	return value[:cut], true
}

func validateFetchURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ErrEmptyURL
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("%w: %q", ErrUnsupportedScheme, u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("%w: missing host", ErrInvalidURL)
	}
	return u.String(), nil
}

func parseMediaType(header string) (mediaType, charset string) {
	if strings.TrimSpace(header) == "" {
		return "", ""
	}
	mediaType, params, err := mime.ParseMediaType(header)
	if err != nil {
		return strings.ToLower(strings.TrimSpace(strings.Split(header, ";")[0])), ""
	}
	return strings.ToLower(mediaType), strings.ToLower(strings.TrimSpace(params["charset"]))
}

func validateFetchContentType(mediaType string) error {
	if mediaType == "" {
		return nil
	}
	if isFetchAllowedMediaType(mediaType) {
		return nil
	}
	return fmt.Errorf("%w: %q", ErrUnexpectedContentType, mediaType)
}

func extractFetchText(mediaType string, body []byte) (title, text string, links []Link, hints htmlPageHints) {
	if isHTMLMediaType(mediaType) || (mediaType == "" && looksLikeHTML(body)) {
		return extractHTMLPage(body)
	}
	switch {
	case isJSONMediaType(mediaType):
		return "", prettyJSON(body), nil, hints
	case isXMLMediaType(mediaType):
		return "", stripXMLTags(body), nil, hints
	default:
		// text/plain, text/markdown, text/csv, and other text/* keep
		// newlines; only inline whitespace runs are collapsed.
		return "", collapseInlineWhitespace(string(body)), nil, hints
	}
}

func isHTMLMediaType(mediaType string) bool {
	return mediaType == "text/html" || mediaType == "application/xhtml+xml"
}

func looksLikeHTML(body []byte) bool {
	// Strip a leading UTF-8 BOM and whitespace so a fragment starting with
	// <head> is still detected.
	s := strings.TrimLeft(string(body), "\xef\xbb\xbf \t\r\n")
	// A body that begins with { or [ is JSON (or JSON-ish), not HTML, even if
	// a string value inside it contains "<head>". This prevents
	// misclassification of JSON payloads describing HTML.
	if len(s) > 0 && (s[0] == '{' || s[0] == '[') {
		return false
	}
	lower := strings.ToLower(s)
	if len(lower) > 512 {
		lower = lower[:512]
	}
	if strings.HasPrefix(lower, "<!doctype html") || strings.HasPrefix(lower, "<html") {
		return true
	}
	return strings.Contains(lower, "<html") || strings.Contains(lower, "<!doctype") || strings.Contains(lower, "<head")
}

func extractHTMLPage(body []byte) (title, text string, links []Link, hints htmlPageHints) {
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return "", collapseInlineWhitespace(stripTagsFallback(string(body))), nil, hints
	}
	title = htmlTitle(doc)

	var b strings.Builder
	var collected []Link
	var walk func(*html.Node, bool)
	walk = func(n *html.Node, inPre bool) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "noscript", "template", "svg", "iframe",
				"nav", "footer", "aside", "form", "dialog":
				return
			case "pre", "code":
				if !inPre {
					if b.Len() > 0 {
						b.WriteByte('\n')
					}
					for child := n.FirstChild; child != nil; child = child.NextSibling {
						walk(child, true)
					}
					b.WriteByte('\n')
					return
				}
			case "a":
				if href := attrValue(n, "href"); isSafeResultURL(href) {
					collected = append(collected, Link{Text: textContent(n), Href: href})
				}
			case "link":
				// llmstxt.org v2 discovery hints: rel="describedby" points
				// at the covering llms.txt; rel="alternate"
				// type="text/markdown" at a markdown version of this page.
				if href := strings.TrimSpace(attrValue(n, "href")); href != "" {
					rel := strings.ToLower(attrValue(n, "rel"))
					typ := strings.ToLower(strings.TrimSpace(attrValue(n, "type")))
					switch {
					case tokenListContains(rel, "describedby"):
						if hints.describedBy == "" {
							hints.describedBy = href
						}
					case tokenListContains(rel, "alternate") && typ == "text/markdown":
						if hints.markdownAlt == "" {
							hints.markdownAlt = href
						}
					}
				}
			}
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			if !inPre {
				b.WriteByte(' ')
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child, inPre)
		}
	}
	walk(doc, false)
	return title, collapseInlineWhitespace(b.String()), collected, hints
}

// htmlTitle resolves the page title with fallbacks: <title> first, then
// og:title / twitter:title meta, then the first <h1>.
func htmlTitle(doc *html.Node) string {
	if t := findDescendant(doc, "title", ""); t != nil {
		if s := strings.TrimSpace(textContent(t)); s != "" {
			return s
		}
	}
	if m := findMetaContent(doc, "property", "og:title"); m != "" {
		return m
	}
	if m := findMetaContent(doc, "name", "twitter:title"); m != "" {
		return m
	}
	if h := findDescendant(doc, "h1", ""); h != nil {
		if s := strings.TrimSpace(textContent(h)); s != "" {
			return s
		}
	}
	return ""
}

// findMetaContent searches for <meta key="val" content="..."> and returns the
// trimmed content. Returns "" if not found.
func findMetaContent(doc *html.Node, key, val string) string {
	var found string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if found != "" {
			return
		}
		if n.Type == html.ElementNode && n.Data == "meta" && attrValue(n, key) == val {
			found = strings.TrimSpace(attrValue(n, "content"))
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return found
}

func stripTagsFallback(raw string) string {
	var b strings.Builder
	inTag := false
	for _, r := range raw {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	return b.String()
}
