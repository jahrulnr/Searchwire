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

`Searcher.Fetch(ctx, url)` / `FetchWithLimit(ctx, url, maxBytes)` perform a single HTTP GET:

| Field | Meaning |
|-------|---------|
| `URL` / `FinalURL` | Requested URL and post-redirect URL |
| `StatusCode` / `ContentType` | Response metadata |
| `Title` / `Text` | Page title (HTML) and readable text |
| `Truncated` / `Bytes` | Whether the body was capped; bytes read |

- Schemes: `http` / `https` only.
- Accepts `text/html`, `application/xhtml+xml`, `text/plain`, and other `text/*`.
- HTML → title + visible text (scripts/styles stripped). Oversized bodies truncate with `Truncated: true` instead of hard-failing.
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
| `searchwire_fetch` | `{ "url": string, "max_bytes"?: number }` | Page JSON (`url`, `finalUrl`, `statusCode`, `contentType`, `title`, `text`, `truncated`, `bytes`) |

Recommended agent workflow: **`searchwire_search` → pick URLs → `searchwire_fetch`**. Snippets alone are incomplete; fetch promising hits for full content.

### Environment

| Variable | Scope | Description |
|----------|-------|-------------|
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

1. Brave Search (HTML)
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
	GitHub: searchwire.GitHubConfig{
		Token: "ghp_...", // or read GITHUB_TOKEN
	},
})
```

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
