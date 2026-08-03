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
	serverName      = "searchwire"
	fallbackVersion = "0.1.0"
	toolName        = "searchwire_search"
	maxErrorLen     = 240
	envLimit        = "SEARCHWIRE_LIMIT"
	envTimeout      = "SEARCHWIRE_TIMEOUT"
	envGitHubToken  = "GITHUB_TOKEN"
	toolDescription = "Metasearch the open web across Brave, Startpage, Wikipedia, and GitHub. " +
		"Returns ranked results with partial failure reporting when some sources fail. " +
		"May take up to ~10s (or SEARCHWIRE_TIMEOUT) because sources are queried concurrently."
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

func main() {
	log.SetOutput(os.Stderr)
	log.SetFlags(0)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := configFromEnv()
	server := newServer(cfg)
	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("searchwire-mcp: %v", err)
	}
}

func newServer(cfg searchwire.Config) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    serverName,
		Version: moduleVersion(),
	}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        toolName,
		Description: toolDescription,
	}, makeSearchHandler(cfg))
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

func runSearch(ctx context.Context, base searchwire.Config, input searchInput) (searchOutput, error) {
	cfg := base
	if input.Limit != nil && *input.Limit > 0 {
		cfg.Limit = *input.Limit
	}

	resp, err := searchwire.New(cfg).Search(ctx, input.Query)
	if err != nil {
		return searchOutput{}, sanitizeSearchErr(err)
	}
	return sanitizeOutput(searchOutput{
		Query:   resp.Query,
		Results: resp.Results,
		Errors:  resp.Errors,
	}), nil
}

func configFromEnv() searchwire.Config {
	cfg := searchwire.Config{}
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
	return cfg
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

func sanitizeOutput(out searchOutput) searchOutput {
	if len(out.Errors) == 0 {
		return out
	}
	errors := make([]searchwire.SourceError, len(out.Errors))
	for i, e := range out.Errors {
		errors[i] = searchwire.SourceError{
			Source: e.Source,
			Error:  sanitizeError(e.Error),
		}
	}
	out.Errors = errors
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
