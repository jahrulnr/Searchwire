package searchwire

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
)

// llms.txt support follows the llmstxt.org v2 proposal: a Markdown file named
// llms.txt at a site root or any subpath that gives agents a concise,
// structured overview of the pages under its path. Chrome Lighthouse audits
// its presence under the Agentic Browsing category. Searchwire implements the
// client side: Fetch surfaces the rel="describedby" / markdown-alternate
// discovery hints, FetchLLMsTxt retrieves and parses the file, and
// ParseLLMsTxt parses llms.txt content directly.
const llmsTxtFileName = "llms.txt"

// LLMsTxt is a parsed /llms.txt file (llmstxt.org v2 format).
type LLMsTxt struct {
	// Title is the required H1 project or site name.
	Title string
	// Summary is the blockquote summary following the H1. Empty when absent.
	Summary string
	// Description is free-form prose between the summary and the first H2
	// section, one trimmed line per entry joined with newlines. Empty when
	// absent.
	Description string
	// Sections are the H2-delimited groups in document order.
	Sections []LLMsSection
}

// LLMsSection is one H2-delimited group of links.
type LLMsSection struct {
	Name  string
	Links []LLMsLink
}

// LLMsLink is one "- [name](url): notes" entry from a section's file list.
type LLMsLink struct {
	Name string
	URL  string
	// Notes is the optional free-text after the ": " separator.
	Notes string
}

// LLMsTxtResult is a fetched and parsed llms.txt file.
type LLMsTxtResult struct {
	// URL is the requested llms.txt URL and FinalURL the post-redirect URL.
	URL      string
	FinalURL string
	// StatusCode is the HTTP status of the successful fetch (2xx family).
	StatusCode int
	// Headers carries selected response headers (same set as Fetch) so
	// callers can do conditional GETs on later refreshes.
	Headers map[string]string
	// LLMsTxt is the parsed file content.
	LLMsTxt *LLMsTxt
}

// ParseLLMsTxt parses llms.txt Markdown content per the llmstxt.org v2
// format: an optional BOM, a required H1 title, an optional blockquote
// summary, optional free-form description, then H2-delimited sections whose
// lists contain "[name](url)" links with optional ": notes". Parsing is
// lenient: unrecognized lines are ignored rather than rejected. It returns an
// error wrapping ErrInvalidLLMsTxt only when the required H1 is missing.
func ParseLLMsTxt(content string) (*LLMsTxt, error) {
	content = strings.TrimPrefix(content, "\ufeff")
	file := &LLMsTxt{}
	var cur *LLMsSection
	var desc []string
	var summary []string
	seenTitle := false

	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" {
			continue
		}
		switch {
		case !seenTitle && strings.HasPrefix(line, "# "):
			file.Title = strings.TrimSpace(line[2:])
			seenTitle = true
		case strings.HasPrefix(line, "## ") || line == "##":
			name := strings.TrimSpace(strings.TrimPrefix(line, "##"))
			file.Sections = append(file.Sections, LLMsSection{Name: name})
			cur = &file.Sections[len(file.Sections)-1]
		case cur == nil && strings.HasPrefix(line, ">"):
			if q := strings.TrimSpace(strings.TrimPrefix(line, ">")); q != "" {
				summary = append(summary, q)
			}
		default:
			if link, ok := parseLLMsListItem(line); ok && cur != nil {
				cur.Links = append(cur.Links, link)
				continue
			}
			if cur == nil && seenTitle {
				desc = append(desc, line)
			}
			// Lines before the H1 and non-list lines inside a section are
			// ignored (lenient parsing).
		}
	}
	if !seenTitle || file.Title == "" {
		return nil, fmt.Errorf("%w: missing required H1 heading", ErrInvalidLLMsTxt)
	}
	file.Summary = strings.Join(summary, " ")
	file.Description = strings.Join(desc, "\n")
	return file, nil
}

// parseLLMsListItem parses one "- [name](url)" or "- [name](url): notes"
// list item. The "*"/"+" markers are accepted too, mirroring common
// Markdown output. Returns ok=false for anything else.
func parseLLMsListItem(line string) (LLMsLink, bool) {
	if line == "" {
		return LLMsLink{}, false
	}
	if line[0] != '-' && line[0] != '*' && line[0] != '+' {
		return LLMsLink{}, false
	}
	rest := strings.TrimSpace(line[1:])
	open := strings.Index(rest, "[")
	close := strings.Index(rest, "](")
	end := -1
	if close >= 0 {
		end = strings.Index(rest[close+2:], ")")
	}
	if open != 0 || close <= open || end < 0 {
		return LLMsLink{}, false
	}
	rawURL := strings.TrimSpace(rest[close+2 : close+2+end])
	if i := strings.Index(rawURL, "\""); i >= 0 {
		// Drop a Markdown "(url \"title\")" suffix; the spec form is
		// "(url): notes" but some generators emit titles.
		rawURL = strings.TrimSpace(rawURL[:i])
	}
	if rawURL == "" {
		return LLMsLink{}, false
	}
	link := LLMsLink{
		Name: strings.TrimSpace(rest[open+1 : close]),
		URL:  rawURL,
	}
	if tail := strings.TrimSpace(rest[close+2+end+1:]); strings.HasPrefix(tail, ":") {
		link.Notes = strings.TrimSpace(tail[1:])
	}
	return link, true
}

// FetchLLMsTxt retrieves and parses the llms.txt file covering rawURL. Per
// the llmstxt.org v2 proposal a file covers every URL under its path, so the
// enclosing directory's llms.txt is probed first and the site root second;
// the most specific file found wins. Directories without their own file
// typically 404 and fall through to the root probe.
//
// When every candidate 404s/410s, the error wraps ErrLLMsTxtNotFound. A file
// that exists but lacks the required H1 wraps ErrInvalidLLMsTxt. Other
// errors (network failures, non-404 HTTP errors, unsupported content types)
// surface as-is. Conditional GET support matches Fetch via LLMsTxtResult.Headers.
func (s *Searcher) FetchLLMsTxt(ctx context.Context, rawURL string) (*LLMsTxtResult, error) {
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
	candidates, err := llmsTxtCandidates(canonical)
	if err != nil {
		return nil, err
	}

	var firstErr error
	for _, cand := range candidates {
		res, err := s.FetchWithOptions(ctx, cand, FetchOptions{})
		if err != nil {
			var httpErr *HTTPError
			if errors.As(err, &httpErr) &&
				(httpErr.StatusCode == 404 || httpErr.StatusCode == 410) {
				continue
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		parsed, perr := ParseLLMsTxt(res.Text)
		if perr != nil {
			// Soft-404 fallbacks (e.g. an SPA serving HTML with 200) land
			// here; try the next candidate but keep the real cause.
			if firstErr == nil {
				firstErr = fmt.Errorf("%w [%s]", perr, res.FinalURL)
			}
			continue
		}
		return &LLMsTxtResult{
			URL:        res.URL,
			FinalURL:   res.FinalURL,
			StatusCode: res.StatusCode,
			Headers:    res.Headers,
			LLMsTxt:    parsed,
		}, nil
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return nil, fmt.Errorf("%w: tried %s", ErrLLMsTxtNotFound, strings.Join(candidates, ", "))
}

// llmsTxtCandidates returns the llms.txt URLs to probe for a page URL, most
// specific first: the enclosing directory, then the site root.
func llmsTxtCandidates(canonical string) ([]string, error) {
	u, err := url.Parse(canonical)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}
	dir := path.Dir(u.Path)
	if dir == "." || dir == "/" {
		dir = ""
	}
	base := u.Scheme + "://" + u.Host
	root := base + "/" + llmsTxtFileName
	if dir == "" {
		return []string{root}, nil
	}
	return []string{base + dir + "/" + llmsTxtFileName, root}, nil
}

// htmlPageHints holds the llmstxt.org v2 discovery links found in an HTML
// document: the covering llms.txt (rel="describedby") and a markdown
// alternate version of the page (rel="alternate" type="text/markdown").
// Values are raw (possibly relative) hrefs; Fetch resolves them.
type htmlPageHints struct {
	describedBy string
	markdownAlt string
}

// llmsTxtAuto is the internal return shape of maybeFetchLLMsTxt: parsed
// content + the final URL it came from. Either field is empty when the
// probe produced nothing useful.
type llmsTxtAuto struct {
	parsed *LLMsTxt
	url    string
}

// maybeFetchLLMsTxt is the best-effort auto-attach used by Fetch. It
// returns nil (no error) unless a probe is actually warranted:
//
//   - The source fetch must have succeeded — non-2xx source responses skip
//     the probe entirely. This matches the spec: an llms.txt is supposed
//     to cover real, accessible pages, not 404s or 5xx.
//   - The response media type must be HTML (text/html or
//     application/xhtml+xml). Other formats (JSON, plain text, markdown,
//     CSV, XML, RSS, Atom) skip the probe — they are not "pages" in the
//     llmstxt.org v2 sense and following a JSON pointer to /llms.txt
//     would be noise.
//   - When the probe is run, it follows the same most-specific-then-root
//     order as Searcher.FetchLLMsTxt, swallows 404/410/soft-404, and
//     returns nil on any non-fatal failure.
//
// ctx is the caller's context so a cancelled fetch short-circuits the
// probe without an extra round-trip.
//
// Implementation note: the probe deliberately uses a raw HTTP request
// rather than recursing through FetchWithOptions. Otherwise a fixture
// (or a real site) that returns the same HTML for every URL would cause
// infinite recursion: page -> /llms.txt -> page -> /llms.txt -> ...
func (s *Searcher) maybeFetchLLMsTxt(ctx context.Context, finalURL, sourceMediaType string) llmsTxtAuto {
	if s == nil {
		return llmsTxtAuto{}
	}
	// Only attach to successful HTML pages. Non-HTML content types and
	// non-2xx statuses (already filtered by the caller before this helper
	// is invoked, but the early-exit makes the contract explicit) are
	// skipped without an HTTP round-trip.
	if !isHTMLMediaType(sourceMediaType) {
		return llmsTxtAuto{}
	}
	candidates, err := llmsTxtCandidates(finalURL)
	if err != nil || len(candidates) == 0 {
		return llmsTxtAuto{}
	}
	for _, cand := range candidates {
		// Honour cancellation between probes.
		if err := ctx.Err(); err != nil {
			return llmsTxtAuto{}
		}
		parsed, probeURL, status, ok := s.probeLLMsTxtOnce(ctx, cand)
		if !ok {
			// Invalid URL / network failure / server error: the probe
			// is not recoverable, stop. The source fetch already
			// succeeded so the caller still gets a useful result;
			// LLMsTxt stays nil.
			return llmsTxtAuto{}
		}
		if status == 404 || status == 410 {
			// The spec (and Searcher.FetchLLMsTxt) treat 404/410 as
			// "no file at this level; try the next candidate". This is
			// the only fallthrough case: a 5xx or any other non-2xx
			// would be a server problem, not a missing file.
			continue
		}
		if parsed != nil {
			return llmsTxtAuto{parsed: parsed, url: probeURL}
		}
		// Soft 404 (SPA returns 2xx HTML at this URL) or invalid file
		// content: try the next candidate.
	}
	return llmsTxtAuto{}
}

// probeLLMsTxtOnce performs a single low-level HTTP probe of a candidate
// llms.txt URL and returns:
//
//   - parsed: the parsed LLMsTxt on success, or nil when the body did
//     not parse (soft 404, SPA fallback, or content type outside the
//     allowlist).
//   - probeURL: the post-redirect URL of the response (always populated
//     when ok is true; useful for diagnostics when parsed is nil).
//   - status: the HTTP status code (0 when no response was received).
//   - ok: true when the request completed and the caller can decide
//     what to do next; false on any non-recoverable error (invalid URL,
//     network failure, body too large, non-2xx / non-4xx status).
//
// The probe deliberately bypasses FetchWithOptions so the auto-attach
// path does not recurse (FetchWithOptions would call this helper again).
func (s *Searcher) probeLLMsTxtOnce(ctx context.Context, cand string) (*LLMsTxt, string, int, bool) {
	if err := ctx.Err(); err != nil {
		return nil, "", 0, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cand, nil)
	if err != nil {
		return nil, "", 0, false
	}
	req.Header.Set("Accept", "text/markdown, text/plain, text/*;q=0.8, */*;q=0.1")
	req.Header.Set("User-Agent", s.userAgent)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, "", 0, false
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, s.maxRawResponseSize+1))
	if err != nil {
		return nil, "", 0, false
	}
	if int64(len(body)) > s.maxRawResponseSize {
		return nil, "", 0, false
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		// Non-2xx: return status so the caller can decide whether the
		// specific code warrants a fallthrough (404/410 do).
		return nil, respFinalURL(resp), resp.StatusCode, true
	}

	mediaType, charset := parseMediaType(resp.Header.Get("Content-Type"))
	body = decodeBody(body, charset)

	// Allow text/markdown, text/plain, and the absence of a content type
	// (some sites serve /llms.txt with no header). Reject anything else
	// (HTML, JSON, etc.) to avoid misclassifying soft-404 pages as the
	// real llms.txt.
	if mediaType != "" && !isTextLikeMediaType(mediaType) {
		return nil, respFinalURL(resp), resp.StatusCode, true
	}

	parsed, perr := ParseLLMsTxt(string(body))
	if perr != nil {
		// Body parsed (2xx) but not as llms.txt: let the caller try the
		// next candidate. probeURL is still useful for diagnostics.
		return nil, respFinalURL(resp), resp.StatusCode, true
	}
	return parsed, respFinalURL(resp), resp.StatusCode, true
}

// respFinalURL resolves the post-redirect URL of a response.
func respFinalURL(resp *http.Response) string {
	if resp == nil {
		return ""
	}
	if resp.Request != nil && resp.Request.URL != nil {
		return resp.Request.URL.String()
	}
	return ""
}

// isTextLikeMediaType is the allowlist for plain-text endpoint probes
// (llms.txt, markdown alternates): markdown, plain text, and other text/*
// are acceptable; HTML, JSON, XML etc. are rejected so a soft-404 page is
// not mistaken for the text resource. Unknown / missing types are accepted
// by the caller (some servers omit Content-Type on .txt responses).
func isTextLikeMediaType(mt string) bool {
	if isHTMLMediaType(mt) {
		return false
	}
	return strings.HasPrefix(mt, "text/") || mt == "application/markdown"
}

// findLinkHeader scans RFC 8288 Link header values for the first field whose
// rel contains wantRel (and, when wantType is non-empty, whose type parameter
// equals wantType). Returns the raw target URL, or "" when absent.
func findLinkHeader(linkHeaders []string, wantRel, wantType string) string {
	for _, header := range linkHeaders {
		for _, field := range splitLinkHeader(header) {
			open := strings.Index(field, "<")
			close := -1
			params := ""
			if open >= 0 {
				close = strings.Index(field[open:], ">")
				if close >= 0 {
					params = field[open+close+1:]
					field = field[open+1 : open+close]
				}
			}
			if open < 0 || close < 0 {
				continue
			}
			target := strings.TrimSpace(field)
			if target == "" {
				continue
			}
			rel, typ := "", ""
			for _, param := range strings.Split(params, ";") {
				param = strings.TrimSpace(param)
				if param == "" {
					continue
				}
				key, value, found := strings.Cut(param, "=")
				if !found {
					continue
				}
				value = strings.Trim(strings.TrimSpace(value), "\"")
				switch strings.ToLower(strings.TrimSpace(key)) {
				case "rel":
					rel = strings.ToLower(value)
				case "type":
					typ = strings.ToLower(value)
				}
			}
			if !tokenListContains(rel, wantRel) {
				continue
			}
			if wantType != "" && typ != wantType {
				continue
			}
			return target
		}
	}
	return ""
}

// splitLinkHeader splits one Link header value on commas that are outside
// angle brackets, keeping URI references intact.
func splitLinkHeader(header string) []string {
	var fields []string
	var b strings.Builder
	inAngle := false
	for _, r := range header {
		switch {
		case r == '<':
			inAngle = true
			b.WriteRune(r)
		case r == '>':
			inAngle = false
			b.WriteRune(r)
		case r == ',' && !inAngle:
			fields = append(fields, b.String())
			b.Reset()
		default:
			b.WriteRune(r)
		}
	}
	fields = append(fields, b.String())
	return fields
}

// tokenListContains reports whether a whitespace-separated token list (such
// as a rel attribute or parameter) contains tok.
func tokenListContains(list, tok string) bool {
	for _, part := range strings.Fields(list) {
		if part == tok {
			return true
		}
	}
	return false
}

// firstNonEmpty returns the first non-empty argument, or "" when all are.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// resolveRef resolves ref against the base document URL, returning an
// absolute URL. Empty or unresolvable refs yield "".
func resolveRef(base, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	u, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	if u.IsAbs() {
		return ref
	}
	b, err := url.Parse(base)
	if err != nil {
		return ""
	}
	return b.ResolveReference(u).String()
}
