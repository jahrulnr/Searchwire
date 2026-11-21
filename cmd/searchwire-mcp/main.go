// Command searchwire-mcp exposes Searchwire as an MCP stdio server.
package main

import (
	"context"
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
	// Tool names drop the "searchwire_" prefix because the MCP server
	// name already carries it. Clients address tools as
	// "<server-name>.<tool>" (e.g. "searchwire.search"), so a prefixed
	// tool name would be doubly redundant: "searchwire.searchwire_search".
	maxErrorLen         = 240
	envLimit            = "SEARCHWIRE_LIMIT"
	envTimeout          = "SEARCHWIRE_TIMEOUT"
	envAnswerTimeout    = "SEARCHWIRE_ANSWER_TIMEOUT"
	envMaxBytes         = "SEARCHWIRE_MAX_BYTES"
	envTempDir          = "SEARCHWIRE_TEMP_DIR"
	envDefaultSources   = "SEARCHWIRE_DEFAULT_SOURCES"
	envGitHubToken      = "GITHUB_TOKEN"
	envStackExchangeKey = "STACKEXCHANGE_API_KEY"
	envOpenAlexKey      = "OPENALEX_API_KEY"
	envS2Key            = "SEMANTIC_SCHOLAR_API_KEY"
	envBraveAPIKey      = "BRAVE_SEARCH_API_KEY"
	envOpenRouterAPIKey = "OPENROUTER_API_KEY"
	envOpenAIAPIKey     = "OPENAI_API_KEY"
	envPerplexityAPIKey = "PERPLEXITY_API_KEY"
	envAnthropicAPIKey  = "ANTHROPIC_API_KEY"
	envXAIAPIKey        = "XAI_API_KEY"
	envOmniAPIKey       = "OMNI_API_KEY"
	envOmniBaseURL      = "OMNI_BASE_URL"
	envOpenCodeAPIKey   = "OPENCODE_API_KEY"
	envOpenCode2APIKey  = "OPENCODE2_API_KEY"
)

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

// newServer registers every tool the configured Searcher exposes. Tool names,
// descriptions, schemas, and execution all live in the library so other hosts
// share one contract; this binary only adapts them to the MCP wire.
func newServer(cfg searchwire.Config) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    serverName,
		Version: moduleVersion(),
	}, nil)
	for _, def := range searchwire.New(cfg).Tools() {
		server.AddTool(&mcp.Tool{
			Name:        def.Name,
			Description: def.Description,
			InputSchema: def.InputSchema,
		}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			out, err := def.Run(ctx, req.Params.Arguments)
			if err != nil {
				return &mcp.CallToolResult{
					IsError: true,
					Content: []mcp.Content{&mcp.TextContent{Text: sanitizeError(err.Error())}},
				}, nil
			}
			return &mcp.CallToolResult{
				Content:           []mcp.Content{&mcp.TextContent{Text: string(out)}},
				StructuredContent: out,
			}, nil
		})
	}
	return server
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
	if raw := strings.TrimSpace(os.Getenv(envAnswerTimeout)); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d < 0 {
			log.Printf("searchwire-mcp: ignoring invalid %s=%q", envAnswerTimeout, raw)
		} else if d > 0 {
			cfg.AnswerTimeout = d
		}
	}
	if raw := strings.TrimSpace(os.Getenv(envMaxBytes)); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			log.Printf("searchwire-mcp: ignoring invalid %s=%q", envMaxBytes, raw)
		} else if n > 0 {
			cfg.MaxResponseBytes = n
		}
	}
	if raw := strings.TrimSpace(os.Getenv(envTempDir)); raw != "" {
		cfg.TempDir = raw
	}
	if raw := strings.TrimSpace(os.Getenv(envDefaultSources)); raw != "" {
		for name := range strings.SplitSeq(raw, ",") {
			if name = strings.TrimSpace(name); name != "" {
				cfg.DefaultSources = append(cfg.DefaultSources, name)
			}
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
		strings.TrimSpace(os.Getenv(envStackExchangeKey)),
		strings.TrimSpace(os.Getenv(envOpenAlexKey)),
		strings.TrimSpace(os.Getenv(envS2Key)),
		strings.TrimSpace(os.Getenv(envBraveAPIKey)),
		strings.TrimSpace(os.Getenv(envOpenRouterAPIKey)),
		strings.TrimSpace(os.Getenv(envOpenAIAPIKey)),
		strings.TrimSpace(os.Getenv(envPerplexityAPIKey)),
		strings.TrimSpace(os.Getenv(envAnthropicAPIKey)),
		strings.TrimSpace(os.Getenv(envXAIAPIKey)),
		strings.TrimSpace(os.Getenv(envOmniAPIKey)),
		strings.TrimSpace(os.Getenv(envOpenCodeAPIKey)),
		strings.TrimSpace(os.Getenv(envOpenCode2APIKey)),
		strings.TrimSpace(os.Getenv("OPENAI")),
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
