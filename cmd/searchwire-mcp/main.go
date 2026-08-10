// Command searchwire-mcp exposes Searchwire as an MCP stdio server.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jahrulnr/searchwire"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	serverName          = "searchwire"
	fallbackVersion     = "0.1.0"
	searchToolName      = "searchwire_search"
	fetchToolName       = "searchwire_fetch"
	answerToolName      = "searchwire_answer"
	maxErrorLen         = 240
	envLimit            = "SEARCHWIRE_LIMIT"
	envTimeout          = "SEARCHWIRE_TIMEOUT"
	envMaxBytes         = "SEARCHWIRE_MAX_BYTES"
	envGitHubToken      = "GITHUB_TOKEN"
	envBraveAPIKey      = "BRAVE_SEARCH_API_KEY"
	envOpenRouterAPIKey = "OPENROUTER_API_KEY"
	envOpenAIAPIKey     = "OPENAI_API_KEY"
	envPerplexityAPIKey = "PERPLEXITY_API_KEY"
	envAnthropicAPIKey  = "ANTHROPIC_API_KEY"
	envXAIAPIKey        = "XAI_API_KEY"
	searchDescription   = "Metasearch the open web across Brave, Startpage, Wikipedia, and GitHub. " +
		"Returns ranked results with partial failure reporting when some sources fail. " +
		"May take up to ~10s (or SEARCHWIRE_TIMEOUT) because sources are queried concurrently. " +
		"After searching, use searchwire_fetch on promising URLs for full page text."
	fetchDescription = "Fetch one http(s) URL and return readable text (HTML stripped to title + visible text, " +
		"or plain text as-is). Prefer searchwire_search first, then fetch promising result URLs. " +
		"Not a full browser: no JavaScript rendering. max_bytes caps the length of the returned " +
		"readable text (not the raw HTML download); over-limit text is truncated with truncated=true."
	answerDescription = "Answer one question using an optional web-grounded answer provider. " +
		"This tool is only exposed when a provider API key is configured."
)

type searchInput struct {
	Query string `json:"query" jsonschema:"the search query"`
	Limit *int   `json:"limit,omitempty" jsonschema:"optional maximum number of results"`
}

type searchOutput struct {
	Query   string                   `json:"query"`
	Results []searchwire.Result      `json:"results"`
	Errors  []searchwire.SourceError `json:"errors,omitempty"`
}

type fetchInput struct {
	URL      string `json:"url" jsonschema:"absolute http or https URL to fetch"`
	MaxBytes *int64 `json:"max_bytes,omitempty" jsonschema:"optional budget for the returned readable text (bytes); capped by adapter/library limit"`
}

type fetchOutput struct {
	URL         string `json:"url"`
	FinalURL    string `json:"finalUrl,omitempty"`
	StatusCode  int    `json:"statusCode"`
	ContentType string `json:"contentType,omitempty"`
	Title       string `json:"title,omitempty"`
	Text        string `json:"text"`
	Truncated   bool   `json:"truncated"`
	Bytes       int    `json:"bytes"` // byte length of the returned Text (after extraction)
}

type answerInput struct {
	Question string `json:"question" jsonschema:"the question to answer using current web information"`
	Provider string `json:"provider,omitempty" jsonschema:"optional configured provider: brave, openrouter, openai, perplexity, anthropic, or xai"`
}

type answerOutput struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
	Provider string `json:"provider"`
}

func main() {
	log.SetOutput(os.Stderr)
	log.SetFlags(0)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, maxBytes := configFromEnv()
	server := newServer(cfg, maxBytes)
	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("searchwire-mcp: %v", err)
	}
}

func newServer(cfg searchwire.Config, defaultMaxBytes int64) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    serverName,
		Version: moduleVersion(),
	}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        searchToolName,
		Description: searchDescription,
	}, makeSearchHandler(cfg))
	mcp.AddTool(server, &mcp.Tool{
		Name:        fetchToolName,
		Description: fetchDescription,
	}, makeFetchHandler(cfg, defaultMaxBytes))
	if searchwire.New(cfg).CanAnswer() {
		mcp.AddTool(server, &mcp.Tool{
			Name:        answerToolName,
			Description: answerDescription,
		}, makeAnswerHandler(cfg))
	}
	return server
}

func makeSearchHandler(base searchwire.Config) mcp.ToolHandlerFor[searchInput, searchOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, input searchInput) (*mcp.CallToolResult, searchOutput, error) {
		out, err := runSearch(ctx, base, input)
		if err != nil {
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: sanitizeError(err.Error())}},
			}, searchOutput{}, nil
		}
		text, marshalErr := json.MarshalIndent(out, "", "  ")
		if marshalErr != nil {
			return nil, out, nil
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(text)}},
		}, out, nil
	}
}

func makeFetchHandler(base searchwire.Config, defaultMaxBytes int64) mcp.ToolHandlerFor[fetchInput, fetchOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, input fetchInput) (*mcp.CallToolResult, fetchOutput, error) {
		out, err := runFetch(ctx, base, defaultMaxBytes, input)
		if err != nil {
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: sanitizeError(err.Error())}},
			}, fetchOutput{}, nil
		}
		text, marshalErr := json.MarshalIndent(out, "", "  ")
		if marshalErr != nil {
			return nil, out, nil
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(text)}},
		}, out, nil
	}
}

func makeAnswerHandler(base searchwire.Config) mcp.ToolHandlerFor[answerInput, answerOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, input answerInput) (*mcp.CallToolResult, answerOutput, error) {
		out, err := runAnswer(ctx, base, input)
		if err != nil {
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: sanitizeError(err.Error())}},
			}, answerOutput{}, nil
		}
		text, marshalErr := json.MarshalIndent(out, "", "  ")
		if marshalErr != nil {
			return nil, out, nil
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(text)}},
		}, out, nil
	}
}

func runSearch(ctx context.Context, base searchwire.Config, input searchInput) (searchOutput, error) {
	cfg := base
	if input.Limit != nil && *input.Limit > 0 {
		cfg.Limit = *input.Limit
	}

	resp, err := searchwire.New(cfg).Search(ctx, input.Query)
	if err != nil {
		return searchOutput{}, sanitizeSearchErr(err)
	}
	return sanitizeSearchOutput(searchOutput{
		Query:   resp.Query,
		Results: resp.Results,
		Errors:  resp.Errors,
	}), nil
}

func runFetch(ctx context.Context, base searchwire.Config, defaultMaxBytes int64, input fetchInput) (fetchOutput, error) {
	cfg := base
	limit := defaultMaxBytes
	if input.MaxBytes != nil && *input.MaxBytes > 0 {
		limit = *input.MaxBytes
		if cfg.MaxResponseBytes > 0 && limit > cfg.MaxResponseBytes {
			limit = cfg.MaxResponseBytes
		}
	}

	result, err := searchwire.New(cfg).FetchWithLimit(ctx, input.URL, limit)
	if err != nil {
		return fetchOutput{}, sanitizeFetchErr(err)
	}
	return fetchOutput{
		URL:         result.URL,
		FinalURL:    result.FinalURL,
		StatusCode:  result.StatusCode,
		ContentType: result.ContentType,
		Title:       result.Title,
		Text:        result.Text,
		Truncated:   result.Truncated,
		Bytes:       result.Bytes,
	}, nil
}

func runAnswer(ctx context.Context, base searchwire.Config, input answerInput) (answerOutput, error) {
	answer, err := searchwire.New(base).AnswerWithOptions(ctx, input.Question, searchwire.AnswerOptions{Provider: input.Provider})
	if err != nil {
		return answerOutput{}, sanitizeAnswerErr(err)
	}
	return answerOutput{
		Question: answer.Question,
		Answer:   answer.Text,
		Provider: answer.Provider,
	}, nil
}

func configFromEnv() (searchwire.Config, int64) {
	cfg := searchwire.Config{}
	var maxBytes int64
	if raw := strings.TrimSpace(os.Getenv(envLimit)); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			log.Printf("searchwire-mcp: ignoring invalid %s=%q", envLimit, raw)
		} else if n > 0 {
			cfg.Limit = n
		}
	}
	if raw := strings.TrimSpace(os.Getenv(envTimeout)); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d < 0 {
			log.Printf("searchwire-mcp: ignoring invalid %s=%q", envTimeout, raw)
		} else if d > 0 {
			cfg.Timeout = d
		}
	}
	if raw := strings.TrimSpace(os.Getenv(envMaxBytes)); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			log.Printf("searchwire-mcp: ignoring invalid %s=%q", envMaxBytes, raw)
		} else if n > 0 {
			cfg.MaxResponseBytes = n
			maxBytes = n
		}
	}
	return cfg, maxBytes
}

func moduleVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok || info == nil {
		return fallbackVersion
	}
	if v := strings.TrimSpace(info.Main.Version); v != "" && v != "(devel)" {
		return strings.TrimPrefix(v, "v")
	}
	for _, dep := range info.Deps {
		if dep != nil && dep.Path == "github.com/jahrulnr/searchwire" {
			if v := strings.TrimSpace(dep.Version); v != "" && v != "(devel)" {
				return strings.TrimPrefix(v, "v")
			}
		}
	}
	return fallbackVersion
}

func sanitizeSearchErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, searchwire.ErrEmptyQuery) {
		return searchwire.ErrEmptyQuery
	}
	if errors.Is(err, searchwire.ErrNilSearcher) {
		return searchwire.ErrNilSearcher
	}
	var searchErr *searchwire.SearchError
	if errors.As(err, &searchErr) {
		failures := make([]searchwire.SourceError, len(searchErr.Failures))
		for i, f := range searchErr.Failures {
			failures[i] = searchwire.SourceError{
				Source: f.Source,
				Error:  sanitizeError(f.Error),
			}
		}
		return &searchwire.SearchError{Failures: failures}
	}
	return errors.New(sanitizeError(err.Error()))
}

func sanitizeFetchErr(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, searchwire.ErrEmptyURL),
		errors.Is(err, searchwire.ErrInvalidURL),
		errors.Is(err, searchwire.ErrUnsupportedScheme),
		errors.Is(err, searchwire.ErrNilSearcher),
		errors.Is(err, searchwire.ErrUnexpectedContentType):
		return err
	}
	var httpErr *searchwire.HTTPError
	if errors.As(err, &httpErr) {
		return &searchwire.HTTPError{
			StatusCode: httpErr.StatusCode,
			Status:     httpErr.Status,
			Body:       sanitizeError(httpErr.Body),
		}
	}
	return errors.New(sanitizeError(err.Error()))
}

func sanitizeAnswerErr(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, searchwire.ErrEmptyQuestion),
		errors.Is(err, searchwire.ErrAnswerUnavailable),
		errors.Is(err, searchwire.ErrAnswerProviderUnavailable),
		errors.Is(err, searchwire.ErrInvalidProviderResponse),
		errors.Is(err, searchwire.ErrNilSearcher):
		return err
	}
	return errors.New(sanitizeError(err.Error()))
}

func sanitizeSearchOutput(out searchOutput) searchOutput {
	if len(out.Errors) == 0 {
		return out
	}
	errs := make([]searchwire.SourceError, len(out.Errors))
	for i, e := range out.Errors {
		errs[i] = searchwire.SourceError{
			Source: e.Source,
			Error:  sanitizeError(e.Error),
		}
	}
	out.Errors = errs
	return out
}

func sanitizeError(msg string) string {
	msg = redactSecrets(msg)
	msg = strings.TrimSpace(msg)
	if len(msg) > maxErrorLen {
		return msg[:maxErrorLen] + "…"
	}
	return msg
}

func redactSecrets(msg string) string {
	secrets := []string{
		strings.TrimSpace(os.Getenv(envGitHubToken)),
		strings.TrimSpace(os.Getenv(envBraveAPIKey)),
		strings.TrimSpace(os.Getenv(envOpenRouterAPIKey)),
		strings.TrimSpace(os.Getenv(envOpenAIAPIKey)),
		strings.TrimSpace(os.Getenv(envPerplexityAPIKey)),
		strings.TrimSpace(os.Getenv(envAnthropicAPIKey)),
		strings.TrimSpace(os.Getenv(envXAIAPIKey)),
	}
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		msg = strings.ReplaceAll(msg, secret, "[redacted]")
		msg = strings.ReplaceAll(msg, "Bearer "+secret, "Bearer [redacted]")
	}
	return msg
}
