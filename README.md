# searchwire

Zero-config Go metasearch for agent tooling — usable as a **Go library** or an **MCP stdio server**.

Searchwire runs ordinary web/text searches for you. Provide a query only — no SearXNG deployment, search host, engine selection, or API key is required for the default path. After searching, callers can **fetch** a result URL for readable page text (HTML stripped / plain text).

## Library

```bash
go get github.com/jahrulnr/searchwire
```

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/jahrulnr/searchwire"
)

func main() {
	searcher := searchwire.New(searchwire.Config{})
	resp, err := searcher.Search(context.Background(), "Go context cancellation")
	if err != nil {
		log.Fatal(err)
	}
	for _, result := range resp.Results {
		fmt.Println(result.Title, result.URL)
	}
	for _, sourceErr := range resp.Errors {
		log.Printf("source %s failed: %s", sourceErr.Source, sourceErr.Error)
	}

	if searcher.CanAnswer() {
		answer, err := searcher.Answer(context.Background(), "What changed in Go recently?")
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(answer.Provider, answer.Text)
	}

	if len(resp.Results) > 0 {
		page, err := searcher.Fetch(context.Background(), resp.Results[0].URL)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(page.Title, page.Truncated, len(page.Text))
	}
}
```

`searchwire.New(searchwire.Config{})` and `searchwire.New(searchwire.DefaultConfig())` are equivalent.

### Fetch

`Searcher.Fetch(ctx, url)` / `FetchWithLimit(ctx, url, maxBytes)` /
`FetchWithOptions(ctx, url, opts)` perform a single HTTP GET:

| Field | Meaning |
|-------|---------|
| `URL` / `FinalURL` | Requested URL and post-redirect URL |
| `StatusCode` / `ContentType` | Response metadata |
| `Title` / `Text` | Page title (HTML) and readable text |
| `Links` | Safe http/https anchors collected from HTML (text + href) |
| `Headers` | Selected response headers: `ETag`, `Last-Modified`, `X-RateLimit-Remaining`, `X-RateLimit-Reset`, `Retry-After`, `Content-Range` |
| `Redirects` | Number of redirects followed to reach the final response |
| `Truncated` / `Bytes` | Whether the body was capped; bytes read |

`FetchOptions` enables conditional GET and Range requests:

| Field | Header sent | Notes |
|-------|-------------|-------|
| `MaxBytes` | — | Per-call byte budget (overrides `Config.MaxResponseBytes` when smaller) |
| `IfNoneMatch` | `If-None-Match` | A `304 Not Modified` is returned as a `FetchResult` (not an error) |
| `IfModifiedSince` | `If-Modified-Since` | Same 304 handling |
| `Range` | `Range` | A `206 Partial Content` is returned as a normal `FetchResult` |

- Schemes: `http` / `https` only.
- Accepts `text/html`, `application/xhtml+xml`, `text/plain`, `text/markdown`,
  `text/csv`, `application/json` (and `+json` vendor types), `application/xml`,
  `text/xml`, `application/rss+xml`, `application/atom+xml` (and `+xml` vendor
  types), and other `text/*`.
- HTML → title + visible text (scripts/styles/nav/footer/aside/form stripped,
  `<pre>`/`<code>` whitespace preserved, links collected, `og:title`/`<h1>`
  title fallbacks). JSON → pretty-printed (invalid JSON returned raw).
  XML/RSS/Atom → tag-stripped text with newlines preserved. Markdown/CSV/plain
  text → newlines preserved, inline whitespace collapsed. Non-UTF-8 charsets
  (declared in `Content-Type` or `<meta charset>`) are decoded to UTF-8.
  Oversized bodies truncate with `Truncated: true` and a trailing
  `[truncated]` marker instead of hard-failing.
- Non-2xx responses return `*HTTPError` with `RetryAfter` (parsed
  `Retry-After` seconds, supports integer and HTTP-date forms) and `ErrorBody`
  (parsed JSON error envelope when the error body is JSON).
- Local agent helper only: no JS rendering, no cookie jar, no multi-tenant SSRF suite. Loopback/private hosts are allowed for local-dev agent use.

## MCP Server

Install the stdio adapter:

```bash
go install github.com/jahrulnr/searchwire/cmd/searchwire-mcp@latest
```

Run it as an MCP stdio server (stdout is reserved for the MCP protocol; logs go to stderr):

```bash
searchwire-mcp
```

### Tools

| Name | Input | Output |
|------|-------|--------|
| `searchwire_search` | `{ "query": string, "limit"?: number }` | Searchwire response JSON (`query`, `results`, `errors`) |
| `searchwire_fetch` | `{ "url": string, "max_bytes"?: number }` | Page JSON (`url`, `finalUrl`, `statusCode`, `contentType`, `title`, `text`, `links`, `headers`, `redirects`, `truncated`, `bytes`) |
| `searchwire_answer` | `{ "question": string, "provider"?: string }` | Web-grounded answer JSON (`question`, `answer`, `provider`); registered only when an answer-provider key is available |

Recommended agent workflow: **`searchwire_search` → pick URLs → `searchwire_fetch`**. Snippets alone are incomplete; fetch promising hits for full content.

`searchwire_answer` is capability-driven: it is absent from MCP tool discovery
when none of `BRAVE_SEARCH_API_KEY`, `OPENROUTER_API_KEY`, `OPENAI_API_KEY`,
`PERPLEXITY_API_KEY`, `ANTHROPIC_API_KEY`, or `XAI_API_KEY` is available.
Restart the MCP process after adding or removing a key so clients receive the
updated tool list.
When multiple providers are configured, omit `provider` to use the stable order
Brave → OpenRouter → OpenAI → Perplexity → Anthropic → xAI, or pass a provider
explicitly.

Brave keys must have access to the Answers plan; a Search-only subscription can
still produce a provider billing/authorization error. OpenRouter uses its
current [`openrouter:web_search` server tool](https://openrouter.ai/docs/guides/features/server-tools/web-search)
and defaults to `openrouter/auto`. OpenAI uses the Responses API
[`web_search` tool](https://developers.openai.com/api/docs/guides/tools-web-search)
and defaults to the cost-focused `gpt-5.6-luna`. Override either model with
configuration or its model env var.

### Environment

| Variable | Scope | Description |
|----------|-------|-------------|
| `BRAVE_SEARCH_API_KEY` | library | Optional official Brave Search API key; replaces the Brave HTML adapter when set |
| `OPENROUTER_API_KEY` | library | Optional OpenRouter key; enables the `openrouter` answer provider |
| `OPENROUTER_MODEL` | library | OpenRouter answer model (default `openrouter/auto`) |
| `OPENAI_API_KEY` | library | Optional OpenAI key; enables the `openai` answer provider |
| `OPENAI_MODEL` | library | OpenAI answer model (default `gpt-5.6-luna`) |
| `PERPLEXITY_API_KEY` | library | Optional Perplexity key; enables the `perplexity` answer provider |
| `PERPLEXITY_PRESET` | library | Perplexity Agent API preset (default `low`) |
| `ANTHROPIC_API_KEY` | library | Optional Anthropic key; enables the `anthropic` answer provider |
| `ANTHROPIC_MODEL` | library | Anthropic answer model (default `claude-haiku-4-5`) |
| `XAI_API_KEY` | library | Optional xAI key; enables the `xai` answer provider |
| `XAI_MODEL` | library | xAI answer model (default `grok-4.5`) |
| `GITHUB_TOKEN` | library | Optional GitHub API token (higher rate limits) |
| `SEARCHWIRE_LIMIT` | adapter | Default result limit (int; unset = library default) |
| `SEARCHWIRE_TIMEOUT` | adapter | HTTP client timeout as a Go duration string (e.g. `10s`; unset = library default) |
| `SEARCHWIRE_MAX_BYTES` | adapter | Default fetch/response byte budget (unset = library default) |

Latency: search fans out across 4 sources and may take up to the configured timeout (default 10s). Clients should set appropriate tool timeouts.

### Example MCP client config

Generic stdio MCP entry (Cursor, Claude Desktop, NusaShell custom MCP, etc.):

```json
{
  "command": "searchwire-mcp",
  "args": [],
  "env": {
    "GITHUB_TOKEN": ""
  }
}
```

NusaShell can consume this as an **external** stdio MCP server via manual config. This repo does **not** ship a NusaShell plugin bundle.

## Built-in sources

Searchwire fans out concurrently to:

1. Brave Search (HTML by default; official JSON API when configured)
2. Startpage (HTML)
3. Wikipedia MediaWiki API (JSON)
4. GitHub Search API (repositories + issues; falls back to repositories when issues search fails)

Source ordering is also the tie-break order when fused scores are equal.

Unauthenticated GitHub search is limited to about 10 requests per minute across search endpoints.

## Configuration

`searchwire.Config` groups runtime settings and optional integrations:

```go
searcher := searchwire.New(searchwire.Config{
	Limit:      5,
	HTTPClient: httpClient,
	Brave: searchwire.BraveConfig{
		APIKey: "...", // or read BRAVE_SEARCH_API_KEY
	},
	OpenRouter: searchwire.OpenRouterConfig{
		APIKey: "...", // or read OPENROUTER_API_KEY
		Model:  "openrouter/auto",
	},
	OpenAI: searchwire.OpenAIConfig{
		APIKey: "...", // or read OPENAI_API_KEY
		Model:  "gpt-5.6-luna",
	},
	Perplexity: searchwire.PerplexityConfig{
		APIKey: "...", // or read PERPLEXITY_API_KEY
		Preset: "low",
	},
	Anthropic: searchwire.AnthropicConfig{
		APIKey: "...", // or read ANTHROPIC_API_KEY
		Model:  "claude-haiku-4-5",
	},
	XAI: searchwire.XAIConfig{
		APIKey: "...", // or read XAI_API_KEY
		Model:  "grok-4.5",
	},
	GitHub: searchwire.GitHubConfig{
		Token: "ghp_...", // or read GITHUB_TOKEN
	},
})
```

### Brave

| Field | Default | Description |
|-------|---------|-------------|
| `Enabled` | `true` | Register the Brave source |
| `APIKey` | — | Official Brave Search API key; overrides env |
| `APIKeyEnv` | `BRAVE_SEARCH_API_KEY` | Environment variable used for the key |

With no key, the zero-configuration Brave HTML adapter remains active. When a
key is present, Searchwire replaces it with the official
[Brave Web Search API](https://api-dashboard.search.brave.com/app/documentation/web-search/get-started)
under the same `brave` source name; it does not query both or double-count
duplicate results.

The same resolved key enables `Searcher.CanAnswer()` and `Searcher.Answer()`
using Brave's blocking, single-search
[Answers endpoint](https://api-dashboard.search.brave.com/documentation/services/answers).
The initial answer contract returns provider-grounded text; streaming research
mode and structured citation extraction are not yet exposed.

### LLM answer providers

| Provider | Key env | Model/preset env | Default | Hosted web tool |
|----------|---------|-----------|---------------|-----------------|
| OpenRouter | `OPENROUTER_API_KEY` | `OPENROUTER_MODEL` | `openrouter/auto` | `openrouter:web_search` |
| OpenAI | `OPENAI_API_KEY` | `OPENAI_MODEL` | `gpt-5.6-luna` | Responses API `web_search` |
| Perplexity | `PERPLEXITY_API_KEY` | `PERPLEXITY_PRESET` | `low` preset | Agent API web search + fetch |
| Anthropic | `ANTHROPIC_API_KEY` | `ANTHROPIC_MODEL` | `claude-haiku-4-5` | Messages API web search |
| xAI | `XAI_API_KEY` | `XAI_MODEL` | `grok-4.5` | Responses API web search |

OpenRouter can route web-grounded requests to supported OpenAI, Anthropic,
Google, xAI, and Perplexity model families without adding another Searchwire
credential. Set `OPENROUTER_MODEL` when deterministic model choice or cost is
more important than automatic routing.

### GitHub

| Field | Default | Description |
|-------|---------|-------------|
| `Enabled` | `true` | Register the GitHub source |
| `Token` | — | Bearer token; overrides env |
| `TokenEnv` | `GITHUB_TOKEN` | Env var for token |
| `SearchIssues` | `true` | Repo+issues (B); `false` = repos only (A) |

When `SearchIssues` is true and issues search fails, repository results are still returned.

### Future development

These `Config` fields exist for upcoming work only. **They are ignored by `New()` today.**

| Block | Planned integration |
|-------|---------------------|
| `GoogleConfig` | Google Programmable Search JSON API (`APIKey` / `GOOGLE_API_KEY`, `CX` / `GOOGLE_CX`) |
| `CustomSearchConfig` | Caller-provided search endpoint (`URL`) |

Do not rely on them for behavior until a source adapter lands and tests cover it.

## Partial failures

When at least one source succeeds, `Search` returns a `*Response` with merged results and any source failures in `Response.Errors`. When every source fails, `Search` returns a `*SearchError` listing each failure.

## Limitations

- HTML adapters can break when source markup changes.
- Ordinary text/web results only; no images, news, maps, or shopping categories.
- Fetch is text-oriented (no headless browser / JS execution).
- No CAPTCHA solving, proxy rotation, or anti-bot bypasses.
- Not a promise of SearXNG parity or engine compatibility.

## Development

```bash
gofmt -w *.go cmd/searchwire-mcp/*.go
go mod tidy
go build ./cmd/searchwire-mcp
go install github.com/jahrulnr/searchwire/cmd/searchwire-mcp@latest
go test ./...
go test -race ./...
go vet ./...
SEARCHWIRE_LIVE=1 go test -run TestLiveSearch -v
```

## Identity and license

The metasearch aggregation concept is inspired by systems such as [SearXNG](https://github.com/searxng/searxng), but Searchwire is **not** a SearXNG client, port, configuration-compatible implementation, or source-code derivative. SearXNG is AGPL; Searchwire is MIT.

This library is pre-v1; APIs may change.

MIT License. See [LICENSE](LICENSE).
