package searchwire

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

const (
	searchToolDescription = "Metasearch the open web across Brave, Startpage, Wikipedia, and GitHub. " +
		"Returns ranked results with partial failure reporting when some sources fail. " +
		"May take up to ~10s (or the configured timeout) because sources are queried concurrently. " +
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
		"This tool is only exposed when a provider API key is configured."
)

var (
	searchToolSchema = json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {"type": "string", "description": "the search query"},
			"limit": {"type": "integer", "description": "optional maximum number of results"}
		},
		"required": ["query"],
		"additionalProperties": false
	}`)
	fetchToolSchema = json.RawMessage(`{
		"type": "object",
		"properties": {
			"url": {"type": "string", "description": "absolute http or https URL to fetch"},
			"max_bytes": {"type": "integer", "description": "optional budget for the returned readable text (bytes); capped by adapter/library limit"}
		},
		"required": ["url"],
		"additionalProperties": false
	}`)
	answerToolSchema = json.RawMessage(`{
		"type": "object",
		"properties": {
			"question": {"type": "string", "description": "the question to answer using current web information"},
			"provider": {"type": "string", "description": "optional configured provider: brave, openrouter, openai, perplexity, anthropic, xai, omniroute, or opencode"}
		},
		"required": ["question"],
		"additionalProperties": false
	}`)
)

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
			Description: searchToolDescription,
			InputSchema: searchToolSchema,
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
			InputSchema: answerToolSchema,
			Run:         s.runAnswerTool,
		})
	}
	return tools
}

type toolSearchArgs struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

type toolSearchOutput struct {
	Query   string        `json:"query"`
	Results []Result      `json:"results"`
	Errors  []SourceError `json:"errors,omitempty"`
}

func (s *Searcher) runSearchTool(ctx context.Context, argsJSON json.RawMessage) (json.RawMessage, error) {
	var args toolSearchArgs
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return nil, fmt.Errorf("decode search arguments: %w", err)
	}
	resp, err := s.SearchWithOptions(ctx, args.Query, SearchOptions{Limit: args.Limit})
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
	errs := resp.Errors
	for i, e := range errs {
		errs[i].Error = s.sanitizeToolErrorText(e.Error)
	}
	return json.MarshalIndent(toolSearchOutput{Query: resp.Query, Results: resp.Results, Errors: errs}, "", "  ")
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
	Links       []Link            `json:"links,omitempty"`
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
	FinalURL    string               `json:"finalUrl,omitempty"`
	StatusCode  int                  `json:"statusCode"`
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
	if len(result.Links) > toolFetchMaxLinks {
		out.Links = result.Links[:toolFetchMaxLinks]
		out.LinksOmitted = len(result.Links) - toolFetchMaxLinks
	} else {
		out.Links = result.Links
	}
	if result.LLMsTxt != nil {
		llms := toolLLMsTxtOutput{
			URL:         result.LLMsTxtURL,
			FinalURL:    result.LLMsTxtURL,
			StatusCode:  result.StatusCode,
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
	return json.MarshalIndent(out, "", "  ")
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
	Question string `json:"question"`
	Answer   string `json:"answer"`
	Provider string `json:"provider"`
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
	return json.MarshalIndent(toolAnswerOutput{Question: answer.Question, Answer: answer.Text, Provider: answer.Provider}, "", "  ")
}
