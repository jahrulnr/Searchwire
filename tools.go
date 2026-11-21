package searchwire

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"path"
	"strings"
)

// toolErrorMaxLen bounds error text embedded in tool output (per-source search
// failures, error results) so upstream response bodies cannot bloat the
// document a tool caller receives.
const toolErrorMaxLen = 240

// toolFetchMaxLinks caps the page links echoed into the fetch document — a
// navigation-heavy page can carry hundreds. LinksOmitted records the rest.
const toolFetchMaxLinks = 50

// toolFetchMaxLLMsTxtLinks caps links echoed across all llms.txt sections;
// documentation indexes can carry hundreds of entries. Section names are
// kept in full — only the link lists are bounded.
const toolFetchMaxLLMsTxtLinks = 100

// Tool names are stable identifiers shared by every consumer (the MCP server,
// in-process tool registries, ...). Consumers that embed a different model
// should filter ToolAnswer out when they do not want a second model answering
// on the user's behalf.
const (
	ToolSearch = "search"
	ToolFetch  = "fetch"
	ToolAnswer = "answer"
)

// toolSearchMaxLimit bounds the per-call search limit a tool caller may
// request; larger values only add low-ranked noise and upstream load.
const toolSearchMaxLimit = 50

// toolSnippetMaxLen caps each search snippet in tool output. Some sources
// (Wikipedia extracts, GitHub issue bodies) return whole paragraphs; a
// snippet only has to help the caller decide what to fetch.
const toolSnippetMaxLen = 320

const (
	searchToolDescriptionFmt = "Metasearch the open web across %s. " +
		"Returns ranked results (title, url, snippet, sources, score) with partial failure reporting " +
		"in errors when some sources fail. score is relative to the top hit of this query (top = 1); " +
		"more sources corroborating a hit is the stronger quality signal. " +
		"Sources are queried concurrently, bounded by the configured timeout. " +
		"After searching, use fetch on promising URLs for full page text."
	fetchToolDescription = "Fetch one http(s) URL and return readable text (HTML stripped to title + visible text, " +
		"or plain text as-is). Prefer search first, then fetch promising result URLs. " +
		"Not a full browser: no JavaScript rendering. max_bytes caps the length of the returned " +
		"readable text (not the raw HTML download); over-limit text is truncated with truncated=true " +
		"and the full text is also written to a file under the configured temp dir " +
		"so the agent can read the rest with its file-reading tool. When the page advertises a markdown " +
		"alternate (rel=alternate type=text/markdown), that version is fetched preferentially " +
		"(markdownAltUsed=true). When the source is HTML and the site publishes " +
		"a covering llms.txt, the parsed file is returned inline (llmsTxt field); a site-root catalog " +
		"unrelated to the fetched path is reduced to its header only."
	answerToolDescription = "Answer one question using an optional web-grounded answer provider. " +
		"The answer is composed by the configured provider's own model. " +
		"searchStatus reports observed search activity; not_observed or unknown does not establish grounding. " +
		"This tool is only exposed when a provider API key is configured."
)

var (
	fetchToolSchema = json.RawMessage(`{
		"type": "object",
		"properties": {
			"url": {"type": "string", "minLength": 1, "description": "absolute http or https URL to fetch"},
			"max_bytes": {"type": "integer", "minimum": 1, "description": "optional budget for the returned readable text (bytes); capped by adapter/library limit"}
		},
		"required": ["url"],
		"additionalProperties": false
	}`)
)

// answerToolSchemaFor lists only the providers this Searcher actually has
// configured, so a caller cannot pick one that will fail as unavailable.
func answerToolSchemaFor(providers []string) json.RawMessage {
	provider := map[string]any{
		"type":        "string",
		"description": "optional provider; defaults to " + providers[0],
		"enum":        providers,
	}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"question": map[string]any{"type": "string", "minLength": 1, "description": "the question to answer using current web information"},
			"provider": provider,
		},
		"required":             []string{"question"},
		"additionalProperties": false,
	}
	raw, _ := json.Marshal(schema)
	return raw
}

// searchToolDescriptionFor names the default fan-out and, separately, the
// opt-in sources a caller can request through the sources argument.
func searchToolDescriptionFor(defaults, all []string) string {
	names := "the configured sources"
	if len(defaults) > 0 {
		names = strings.Join(defaults, ", ")
	}
	desc := fmt.Sprintf(searchToolDescriptionFmt, names)
	inDefault := make(map[string]bool, len(defaults))
	for _, n := range defaults {
		inDefault[n] = true
	}
	var extra []string
	for _, n := range all {
		if !inDefault[n] {
			extra = append(extra, n)
		}
	}
	if len(extra) > 0 {
		desc += " Opt-in sources (" + strings.Join(extra, ", ") + ") run only when named in sources; " +
			"use them for academic papers, e.g. sources=[\"openalex\",\"arxiv\",\"crossref\"]. " +
			"Scholarly hits link to the DOI and their snippet starts with [year · venue · citations · open-access PDF]."
	}
	return desc
}

// searchToolSchemaFor builds the search schema with the registered source
// names as the sources enum, so a caller cannot request an unknown one.
func searchToolSchemaFor(all []string) json.RawMessage {
	props := map[string]any{
		"query": map[string]any{"type": "string", "minLength": 1, "description": "the search query"},
		"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": toolSearchMaxLimit,
			"description": "optional maximum number of results (default: configured limit, max 50)"},
	}
	if len(all) > 0 {
		props["sources"] = map[string]any{
			"type":        "array",
			"items":       map[string]any{"type": "string"},
			"uniqueItems": true,
			"description": "optional: query only these sources (in parallel) instead of the default set; may include opt-in sources. Available: " + strings.Join(all, ", ") + ". Unknown names are ignored with a warning.",
		}
	}
	raw, _ := json.Marshal(map[string]any{
		"type":                 "object",
		"properties":           props,
		"required":             []string{"query"},
		"additionalProperties": false,
	})
	return raw
}

// ToolDef is a provider-agnostic tool contract: name, description, JSON
// Schema for the arguments, and a runner that accepts JSON-encoded arguments
// and returns the JSON-encoded result document. Any host (MCP server, agent
// framework tool registry) can adapt these without re-declaring schemas.
type ToolDef struct {
	Name        string
	Description string
	// InputSchema is the JSON Schema object describing the tool arguments.
	InputSchema json.RawMessage
	// Run executes the tool. argsJSON must decode to an object satisfying
	// InputSchema. The result is the JSON document a tool caller receives;
	// errors are already redacted of any configured API keys.
	Run func(ctx context.Context, argsJSON json.RawMessage) (json.RawMessage, error)
}

// Tools returns the tools this Searcher can execute: search and fetch always,
// plus answer when an answer provider is configured. The returned defs bind
// to this Searcher; mutate a copy of Config and build another Searcher for
// different settings.
func (s *Searcher) Tools() []ToolDef {
	tools := []ToolDef{
		{
			Name:        ToolSearch,
			Description: searchToolDescriptionFor(s.DefaultSources(), s.Sources()),
			InputSchema: searchToolSchemaFor(s.Sources()),
			Run:         s.runSearchTool,
		},
		{
			Name:        ToolFetch,
			Description: fetchToolDescription,
			InputSchema: fetchToolSchema,
			Run:         s.runFetchTool,
		},
	}
	if s.CanAnswer() {
		tools = append(tools, ToolDef{
			Name:        ToolAnswer,
			Description: answerToolDescription,
			InputSchema: answerToolSchemaFor(s.AvailableAnswerProviders()),
			Run:         s.runAnswerTool,
		})
	}
	return tools
}

type toolSearchArgs struct {
	Query   string   `json:"query"`
	Limit   int      `json:"limit"`
	Sources []string `json:"sources"`
}

// Tool output uses camelCase keys throughout; the library's Result,
// SourceError, and Link types carry no JSON tags, so they are mapped to
// tagged mirrors here rather than serialized with Go field names.
type toolSearchOutput struct {
	Query   string            `json:"query"`
	Results []toolResult      `json:"results"`
	Errors  []toolSourceError `json:"errors,omitempty"`
	// Warnings reports ignored request details (e.g. unknown source names).
	Warnings []string `json:"warnings,omitempty"`
}

type toolResult struct {
	Title   string   `json:"title"`
	URL     string   `json:"url"`
	Snippet string   `json:"snippet,omitempty"`
	Sources []string `json:"sources"`
	Score   float64  `json:"score"`
}

type toolSourceError struct {
	Source string `json:"source"`
	Error  string `json:"error"`
}

type toolLink struct {
	Text string `json:"text,omitempty"`
	Href string `json:"href"`
}

// marshalTool encodes a tool result compactly: indentation roughly doubles
// the whitespace a model must read for no information gain.
func marshalTool(v any) (json.RawMessage, error) {
	return json.Marshal(v)
}

// truncateSnippet caps text at max bytes on a word boundary when one is
// reasonably close, appending an ellipsis.
func truncateSnippet(text string, max int) string {
	if max <= 0 || len(text) <= max {
		return text
	}
	const ellipsis = "…"
	if max <= len(ellipsis) {
		cut, _ := truncateRunes(text, int64(max))
		return cut
	}
	cut, _ := truncateRunes(text, int64(max-len(ellipsis)))
	if i := strings.LastIndexByte(cut, ' '); i > max*3/4 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,;:.-") + ellipsis
}

func (s *Searcher) runSearchTool(ctx context.Context, argsJSON json.RawMessage) (json.RawMessage, error) {
	var args toolSearchArgs
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return nil, fmt.Errorf("decode search arguments: %w", err)
	}
	if args.Limit > toolSearchMaxLimit {
		args.Limit = toolSearchMaxLimit
	}
	resp, err := s.SearchWithOptions(ctx, args.Query, SearchOptions{Limit: args.Limit, Sources: args.Sources})
	if err != nil {
		var searchErr *SearchError
		if errors.As(err, &searchErr) {
			failures := make([]SourceError, len(searchErr.Failures))
			for i, f := range searchErr.Failures {
				failures[i] = SourceError{Source: f.Source, Error: s.sanitizeToolErrorText(f.Error)}
			}
			return nil, &SearchError{Failures: failures}
		}
		return nil, s.sanitizeToolError(err)
	}
	out := toolSearchOutput{Query: resp.Query, Results: make([]toolResult, len(resp.Results)), Warnings: resp.Warnings}
	for i, r := range resp.Results {
		out.Results[i] = toolResult{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: truncateSnippet(r.Snippet, toolSnippetMaxLen),
			Sources: r.Sources,
			// Three decimals keep the ordering signal without echoing
			// float noise like 0.9838709677419354.
			Score: math.Round(r.Score*1000) / 1000,
		}
	}
	for _, e := range resp.Errors {
		out.Errors = append(out.Errors, toolSourceError{Source: e.Source, Error: s.sanitizeToolErrorText(e.Error)})
	}
	return marshalTool(out)
}

type toolFetchArgs struct {
	URL      string `json:"url"`
	MaxBytes int64  `json:"max_bytes"`
}

type toolFetchOutput struct {
	URL         string            `json:"url"`
	FinalURL    string            `json:"finalUrl,omitempty"`
	StatusCode  int               `json:"statusCode"`
	ContentType string            `json:"contentType,omitempty"`
	Title       string            `json:"title,omitempty"`
	Text        string            `json:"text"`
	Links       []toolLink        `json:"links,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Redirects   int               `json:"redirects,omitempty"`
	Truncated   bool              `json:"truncated"`
	Bytes       int               `json:"bytes"`
	DescribedBy string            `json:"describedBy,omitempty"`
	MarkdownAlt string            `json:"markdownAlt,omitempty"`
	// MarkdownAltUsed is true when Text was read from the advertised
	// markdown alternate rather than extracted from the HTML.
	MarkdownAltUsed bool `json:"markdownAltUsed,omitempty"`
	// LinksOmitted counts links dropped by toolFetchMaxLinks so the caller
	// knows the page had more.
	LinksOmitted int `json:"linksOmitted,omitempty"`
	// LLMsTxt is the parsed llms.txt covering this page when the source
	// fetch was HTML (2xx) and a covering file was retrievable. Empty when
	// the source is not HTML, the fetch failed, or no covering file exists.
	LLMsTxt *toolLLMsTxtOutput `json:"llmsTxt,omitempty"`
	// TempFile holds the full extracted text when Truncated is true, so
	// the agent can read the rest with its file-reading tool without
	// refetching. Empty when not truncated.
	TempFile string `json:"tempFile,omitempty"`
}

type toolLLMsTxtOutput struct {
	URL         string               `json:"url"`
	Title       string               `json:"title"`
	Summary     string               `json:"summary,omitempty"`
	Description string               `json:"description,omitempty"`
	Sections    []toolLLMsTxtSection `json:"sections,omitempty"`
	// LinksOmitted counts llms.txt links not echoed: either dropped by
	// toolFetchMaxLLMsTxtLinks, or omitted entirely when a site-root
	// catalog covers none of the fetched page's directory.
	LinksOmitted int `json:"linksOmitted,omitempty"`
}

type toolLLMsTxtLink struct {
	Name  string `json:"name"`
	URL   string `json:"url"`
	Notes string `json:"notes,omitempty"`
}

type toolLLMsTxtSection struct {
	Name  string            `json:"name"`
	Links []toolLLMsTxtLink `json:"links,omitempty"`
}

func (s *Searcher) runFetchTool(ctx context.Context, argsJSON json.RawMessage) (json.RawMessage, error) {
	var args toolFetchArgs
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return nil, fmt.Errorf("decode fetch arguments: %w", err)
	}
	limit := s.maxResponseSize
	if args.MaxBytes > 0 && (limit <= 0 || args.MaxBytes < limit) {
		limit = args.MaxBytes
	}
	result, err := s.FetchWithLimit(ctx, args.URL, limit)
	if err != nil {
		return nil, s.sanitizeToolError(err)
	}
	out := toolFetchOutput{
		URL:             result.URL,
		FinalURL:        result.FinalURL,
		StatusCode:      result.StatusCode,
		ContentType:     result.ContentType,
		Title:           result.Title,
		Text:            result.Text,
		Headers:         result.Headers,
		Redirects:       result.Redirects,
		Truncated:       result.Truncated,
		Bytes:           result.Bytes,
		DescribedBy:     result.DescribedBy,
		MarkdownAlt:     result.MarkdownAlt,
		MarkdownAltUsed: result.MarkdownAltUsed,
		TempFile:        result.TempFile,
	}
	links := result.Links
	if len(links) > toolFetchMaxLinks {
		out.LinksOmitted = len(links) - toolFetchMaxLinks
		links = links[:toolFetchMaxLinks]
	}
	for _, l := range links {
		text := l.Text
		if text == l.Href {
			text = ""
		}
		out.Links = append(out.Links, toolLink{Text: text, Href: l.Href})
	}
	if result.LLMsTxt != nil {
		llms := toolLLMsTxtOutput{
			URL:         result.LLMsTxtURL,
			Title:       result.LLMsTxt.Title,
			Summary:     result.LLMsTxt.Summary,
			Description: result.LLMsTxt.Description,
		}
		sections := scopedLLMsTxtSections(result.LLMsTxt, result.FinalURL, result.LLMsTxtURL)
		remaining := toolFetchMaxLLMsTxtLinks
		for _, sec := range sections {
			s := toolLLMsTxtSection{Name: sec.Name}
			for _, l := range sec.Links {
				if remaining > 0 {
					s.Links = append(s.Links, toolLLMsTxtLink{Name: l.Name, URL: l.URL, Notes: l.Notes})
					remaining--
				} else {
					llms.LinksOmitted++
				}
			}
			llms.Sections = append(llms.Sections, s)
		}
		if sections == nil {
			for _, sec := range result.LLMsTxt.Sections {
				llms.LinksOmitted += len(sec.Links)
			}
		}
		out.LLMsTxt = &llms
	}
	return marshalTool(out)
}

// scopedLLMsTxtSections decides which llms.txt sections to echo for a
// fetched page. A site-root llms.txt is a domain-wide catalog: when the
// fetched page lives under a subdirectory and no catalog link points into
// that directory on the same host, the whole catalog is unrelated noise
// (e.g. github.com/llms.txt on a repo page) — return nil so the output
// keeps only url/title/summary plus the omitted-link count. A file found
// at the page's enclosing directory is path-scoped by definition and a
// root file fetched for a root page legitimately covers it, so both pass
// through in full.
func scopedLLMsTxtSections(parsed *LLMsTxt, pageURL, llmsURL string) []LLMsSection {
	page, perr := url.Parse(pageURL)
	cat, cerr := url.Parse(llmsURL)
	if perr != nil || cerr != nil || cat.Path != "/"+llmsTxtFileName {
		return parsed.Sections
	}
	dir := path.Dir(page.Path)
	if dir == "." || dir == "/" {
		return parsed.Sections
	}
	prefix := dir + "/"
	total, matched := 0, 0
	for _, sec := range parsed.Sections {
		for _, l := range sec.Links {
			// Catalog links may be relative; resolve them against the
			// llms.txt URL so a root file written with site-relative
			// links still scopes correctly.
			lu, err := cat.Parse(l.URL)
			if err != nil {
				continue
			}
			total++
			if strings.EqualFold(lu.Host, page.Host) && (lu.Path == dir || strings.HasPrefix(lu.Path, prefix)) {
				matched++
			}
		}
	}
	if total > 0 && matched == 0 {
		return nil
	}
	return parsed.Sections
}

type toolAnswerArgs struct {
	Question string `json:"question"`
	Provider string `json:"provider"`
}

type toolAnswerOutput struct {
	Question     string `json:"question"`
	Answer       string `json:"answer"`
	Provider     string `json:"provider"`
	SearchStatus string `json:"searchStatus"`
}

// sanitizeToolError redacts every configured credential from an error while
// preserving its type where redactErrorSecret can (HTTPError keeps fields),
// then bounds the text the error will produce: an upstream error page can
// run to tens of KB and would flood the caller's tool output.
func (s *Searcher) sanitizeToolError(err error) error {
	if err == nil {
		return nil
	}
	for _, secret := range s.secrets {
		err = redactErrorSecret(err, secret)
	}
	if httpErr, ok := err.(*HTTPError); ok {
		if len(httpErr.Body) <= toolErrorMaxLen {
			return err
		}
		bounded := *httpErr
		bounded.Body = httpErr.Body[:toolErrorMaxLen] + "…"
		return &bounded
	}
	if len(err.Error()) > toolErrorMaxLen {
		return errors.New(s.sanitizeToolErrorText(err.Error()))
	}
	return err
}

// sanitizeToolErrorText redacts configured credentials, then trims and caps
// error text embedded in tool output.
func (s *Searcher) sanitizeToolErrorText(msg string) string {
	for _, secret := range s.secrets {
		msg = strings.ReplaceAll(msg, secret, "[redacted]")
		msg = strings.ReplaceAll(msg, "Bearer "+secret, "Bearer [redacted]")
	}
	msg = strings.TrimSpace(msg)
	if len(msg) > toolErrorMaxLen {
		return msg[:toolErrorMaxLen] + "…"
	}
	return msg
}

func (s *Searcher) runAnswerTool(ctx context.Context, argsJSON json.RawMessage) (json.RawMessage, error) {
	var args toolAnswerArgs
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return nil, fmt.Errorf("decode answer arguments: %w", err)
	}
	answer, err := s.AnswerWithOptions(ctx, args.Question, AnswerOptions{Provider: args.Provider})
	if err != nil {
		return nil, s.sanitizeToolError(err)
	}
	return marshalTool(toolAnswerOutput{Question: answer.Question, Answer: answer.Text, Provider: answer.Provider, SearchStatus: answer.SearchStatus})
}
