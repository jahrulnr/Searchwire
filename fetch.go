package searchwire

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

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
	Truncated   bool
	Bytes       int
}

// Fetch retrieves one http(s) URL and returns readable text.
// HTML responses are stripped to title + visible text. Plain text is returned
// as-is. Bodies larger than maxBytes (or Config.MaxResponseBytes) are truncated
// with Truncated set. This is a local agent helper, not a hardened proxy.
func (s *Searcher) Fetch(ctx context.Context, rawURL string) (*FetchResult, error) {
	return s.FetchWithLimit(ctx, rawURL, 0)
}

// FetchWithLimit is Fetch with an optional per-call byte budget.
// When maxBytes <= 0, Config.MaxResponseBytes is used.
func (s *Searcher) FetchWithLimit(ctx context.Context, rawURL string, maxBytes int64) (*FetchResult, error) {
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

	limit := s.maxResponseSize
	if maxBytes > 0 && (limit <= 0 || maxBytes < limit) {
		limit = maxBytes
	}
	if limit <= 0 {
		limit = defaultMaxResponseBytes
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, canonical, nil)
	if err != nil {
		return nil, fmt.Errorf("searchwire: create request: %w", err)
	}
	req.Header.Set("Accept", "text/html, application/xhtml+xml, text/plain, text/*;q=0.8, */*;q=0.1")
	req.Header.Set("User-Agent", s.userAgent)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("searchwire: read response: %w", err)
	}
	truncated := false
	if int64(len(body)) > limit {
		body = body[:limit]
		truncated = true
	}

	contentType := resp.Header.Get("Content-Type")
	mediaType := parseMediaType(contentType)
	if err := validateFetchContentType(mediaType); err != nil {
		return nil, err
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, &HTTPError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Body:       strings.TrimSpace(string(body)),
		}
	}

	finalURL := canonical
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}

	title, text := extractFetchText(mediaType, body)
	return &FetchResult{
		URL:         canonical,
		FinalURL:    finalURL,
		StatusCode:  resp.StatusCode,
		ContentType: contentType,
		Title:       title,
		Text:        text,
		Truncated:   truncated,
		Bytes:       len(body),
	}, nil
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

func parseMediaType(header string) string {
	if strings.TrimSpace(header) == "" {
		return ""
	}
	mediaType, _, err := mime.ParseMediaType(header)
	if err != nil {
		return strings.ToLower(strings.TrimSpace(strings.Split(header, ";")[0]))
	}
	return strings.ToLower(mediaType)
}

func validateFetchContentType(mediaType string) error {
	if mediaType == "" {
		return nil
	}
	switch {
	case mediaType == "text/html",
		mediaType == "application/xhtml+xml",
		mediaType == "text/plain",
		strings.HasPrefix(mediaType, "text/"):
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrUnexpectedContentType, mediaType)
	}
}

func extractFetchText(mediaType string, body []byte) (title, text string) {
	if isHTMLMediaType(mediaType) || (mediaType == "" && looksLikeHTML(body)) {
		return extractHTMLPage(body)
	}
	return "", collapseWhitespace(string(body))
}

func isHTMLMediaType(mediaType string) bool {
	return mediaType == "text/html" || mediaType == "application/xhtml+xml"
}

func looksLikeHTML(body []byte) bool {
	sample := strings.ToLower(strings.TrimSpace(string(body)))
	if len(sample) > 256 {
		sample = sample[:256]
	}
	return strings.Contains(sample, "<html") || strings.Contains(sample, "<!doctype html") || strings.Contains(sample, "<head")
}

func extractHTMLPage(body []byte) (title, text string) {
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return "", collapseWhitespace(stripTagsFallback(string(body)))
	}
	titleNode := findDescendant(doc, "title", "")
	title = textContent(titleNode)

	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "noscript", "template", "svg", "iframe":
				return
			}
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			b.WriteByte(' ')
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	return title, collapseWhitespace(b.String())
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
