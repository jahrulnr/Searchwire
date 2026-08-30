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
| `DescribedBy` | Absolute URL of the covering `llms.txt` (`rel="describedby"` via HTML `<link>` or HTTP `Link:` header); empty when absent |
| `MarkdownAlt` | Absolute URL of a markdown alternate version of this page (`rel="alternate"` `type="text/markdown"`); empty when absent |
| `LLMsTxt` / `LLMsTxtURL` | Parsed covering `llms.txt` (auto-attached on 2xx HTML responses; see [llms.txt](#llmstxt)) and its post-redirect URL. `nil`/empty for non-HTML responses, 4xx/5xx source, or when no file exists. |
| `TempFile` | Absolute path of a file under the configured temp dir (default `os.TempDir()/searchwire`, override via `Config.TempDir` / `SEARCHWIRE_TEMP_DIR`) holding the full extracted text when `Truncated` is true. Lets the agent `file_read` the rest without refetching. Empty when the response was not truncated. |

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

### llms.txt

Searchwire implements the client side of the [llmstxt.org](https://llmstxt.org/) v2 proposal — the convention Chrome Lighthouse audits under its Agentic Browsing category:

- **Discovery on every fetch.** When a page advertises its LLM-friendly entry points, `Fetch` reports them resolved to absolute URLs: `DescribedBy` (the covering `llms.txt`, from `<link rel="describedby">` or the RFC 8288 `Link:` header) and `MarkdownAlt` (a markdown version of the fetched page, from `<link rel="alternate" type="text/markdown">`).
- **Auto-attach on 2xx HTML.** When the source fetch is HTML and the server returns 2xx, `Fetch` probes `<dir>/llms.txt` first and `/llms.txt` second, parses the first one that answers, and embeds it as `FetchResult.LLMsTxt` (with `LLMsTxtURL` for the post-redirect URL). The probe is best-effort and non-fatal: any failure (404/410 fallthrough, soft-404 SPA, 5xx, network error) leaves the field nil and the source fetch still succeeds. **The probe is skipped entirely for non-HTML responses (JSON, plain text, markdown, CSV, XML, RSS, Atom) and for non-2xx source responses** — matching the spec that llms.txt only applies to navigable pages.
- **Standalone library call.** `Searcher.FetchLLMsTxt(ctx, url)` is still available for callers that want the parsed `*LLMsTxt` without going through a full `Fetch` (e.g. to index a site). It uses the same most-specific-then-root candidate order and 404/410 fallthrough.
- **Parsing.** `ParseLLMsTxt(content string)` parses llms.txt Markdown leniently into a structured `LLMsTxt`: required `Title` (H1), optional blockquote `Summary`, free-form `Description`, and `Sections` of `[name](url): notes` links. A file without the H1 returns an error wrapping `ErrInvalidLLMsTxt`.

```go
// Library: auto-attached on every successful HTML fetch.
res, err := searcher.Fetch(ctx, "https://example.com/docs/page.html")
if err != nil {
    log.Fatal(err)
}
if res.LLMsTxt != nil {
    for _, section := range res.LLMsTxt.Sections {
        for _, link := range section.Links {
            fmt.Println(section.Name, link.Name, link.URL)
        }
    }
}

// Library: standalone probe without fetching the page.
llms, err := searcher.FetchLLMsTxt(ctx, "https://example.com/docs/page.html")
if errors.Is(err, searchwire.ErrLLMsTxtNotFound) {
    // site publishes no llms.txt — fall back to ordinary fetching
}
for _, section := range llms.LLMsTxt.Sections {
    for _, link := range section.Links {
        fmt.Println(section.Name, link.Name, link.URL)
    }
}
```

> **MCP tool note.** The previous standalone `searchwire_llms_txt` MCP tool was merged into `searchwire_fetch`: agents now read the embedded `llmsTxt` field on the fetch result instead of making a second call. `Searcher.FetchLLMsTxt` is unchanged for library users.

### Search

`Searcher.Search(ctx, query)` fans the query out to all enabled sources concurrently, deduplicates results by canonical URL (lowercased host, default ports dropped, fragments and `utm_*` / `fbclid` / `gclid` tracking parameters stripped), then ranks the survivors with Reciprocal Rank Fusion (k=60).

```go
type Result struct {
	Title   string
	URL     string // canonical form used for deduplication
	Snippet string // source-provided snippet; GitHub snippets are HTML-stripped and capped at 320 chars
	Sources []string
	Score   float64 // RRF score: higher = more sources agreed on this URL
}
```

When two results tie on score, earlier-configured sources win (see [Built-in sources](#built-in-sources)). Source failures never abort a search while at least one source succeeds — they are reported per-source in `Response.Errors`.

**Per-call limit.** By default each search returns `Config.Limit` results (default **10**) and asks each source for that many candidates. To override it without rebuilding the searcher:

```go
resp, err := searcher.SearchWithOptions(ctx, "query", searchwire.SearchOptions{Limit: 25})
// Limit <= 0 falls back to Config.Limit, then to the built-in default of 10.
```

The same `Limit` value bounds both what each source is asked for and how many fused results come back.

### Defaults

| Setting | Default | Override |
|---------|---------|----------|
| Result limit | 10 | `Config.Limit`, `SearchOptions{Limit}` (per call), `SEARCHWIRE_LIMIT` (adapter) |
| HTTP timeout | 10s | `Config.Timeout`, `SEARCHWIRE_TIMEOUT` (adapter) |
| Extracted-text budget per fetch | 2 MiB | `Config.MaxResponseBytes`, `FetchOptions.MaxBytes` / `FetchWithLimit` (per call), `SEARCHWIRE_MAX_BYTES` (adapter) |
| Raw download ceiling | 10 MiB | `Config.MaxRawResponseBytes` (safety ceiling, independent of the text budget) |

Note: `Config.HTTPClient`, when provided, replaces the internally constructed client entirely — `Config.Timeout` applies only to the internal client.

### Errors

All sentinel errors are package-level values suitable for `errors.Is`:

| Error | Returned by | Meaning |
|-------|-------------|---------|
| `ErrEmptyQuery` | Search | Empty or whitespace-only query |
| `ErrEmptyQuestion` | Answer | Empty question |
| `ErrEmptyURL` / `ErrInvalidURL` | Fetch | Missing or unparseable URL |
| `ErrUnsupportedScheme` | Fetch | Scheme other than http/https |
| `ErrNilSearcher` | all methods | Method called on a nil `*Searcher` |
| `ErrResponseTooLarge` | Fetch | Body exceeded the raw download ceiling |
| `ErrUnexpectedContentType` | Fetch | Content type outside the accepted allowlist |
| `ErrLLMsTxtNotFound` | FetchLLMsTxt | No `llms.txt` at the directory or site root (all probes 404/410) |
| `ErrInvalidLLMsTxt` | FetchLLMsTxt / ParseLLMsTxt | File exists but violates the required structure (missing H1) |
| `ErrAnswerUnavailable` | Answer | No answer provider configured |
| `ErrAnswerProviderUnavailable` | Answer | Requested provider not configured |
| `ErrInvalidProviderResponse` | Answer | Provider returned an unusable response |

Additionally: every-source failure returns `*SearchError` (inspect `.Failures`), and non-2xx responses return `*HTTPError` (inspect with `errors.As`; see Fetch above). Error strings from configured providers have their API keys redacted before returning.

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
| `searchwire_fetch` | `{ "url": string, "max_bytes"?: number }` | Page JSON (`url`, `finalUrl`, `statusCode`, `contentType`, `title`, `text`, `links`, `headers`, `redirects`, `truncated`, `bytes`, `describedBy`, `markdownAlt`, `llmsTxt?`, `tempFile?`) |
| `searchwire_answer` | `{ "question": string, "provider"?: string }` | Web-grounded answer JSON (`question`, `answer`, `provider`); registered only when an answer-provider key is available |

Recommended agent workflow: **`searchwire_search` → pick URLs → `searchwire_fetch`**. Snippets alone are incomplete; fetch promising hits for full content. When the fetched page is HTML and the site publishes a covering `llms.txt`, the parsed file is embedded in `searchwire_fetch`'s `llmsTxt` field so the agent can follow LLM-friendly documentation without a second call. When the response is truncated, the full text is also written to a file under `SEARCHWIRE_TEMP_DIR` (default `$TMPDIR/searchwire`) and surfaced as `tempFile` — the agent can read the rest with `file_read` instead of refetching.

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
| `SERPER_API_KEY` | library | Optional Serper.dev API key; enables the Serper.dev source |
| `TAVILY_API_KEY` | library | Optional Tavily API key; enables the Tavily source |
| `SEARCHWIRE_LIMIT` | adapter | Default result limit (int; unset = library default) |
| `SEARCHWIRE_TIMEOUT` | adapter | HTTP client timeout as a Go duration string (e.g. `10s`; unset = library default) |
| `SEARCHWIRE_MAX_BYTES` | adapter | Default fetch/response byte budget (unset = library default) |
| `SEARCHWIRE_TEMP_DIR` | adapter | Directory used to materialize truncated fetch artifacts (default `$TMPDIR/searchwire`). The full extracted text is written here when `searchwire_fetch` truncates, and surfaced to the agent as the `tempFile` field so it can read the rest with `file_read` (or framework equivalents). |

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
5. Serper.dev (Google Search API; requires SERPER_API_KEY)
6. Tavily (AI search; requires TAVILY_API_KEY)

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
	Serper: searchwire.SerperConfig{
		APIKey: "...", // or read SERPER_API_KEY
	},
	Tavily: searchwire.TavilyConfig{
		APIKey: "...", // or read TAVILY_API_KEY
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

### Serper.dev

| Field | Default | Description |
|-------|---------|-------------|
| `Enabled` | `false` | Register the Serper source |
| `APIKey` | — | Serper.dev API key; overrides env |
| `APIKeyEnv` | `SERPER_API_KEY` | Env var for the key |

Serper.dev exposes a [Google Search API](https://serper.dev) that returns organic
results as JSON. The source is opt-in: it is only registered when a key resolves
and `Enabled` is not explicitly false. There is no HTML fallback.

### Tavily

| Field | Default | Description |
|-------|---------|-------------|
| `Enabled` | `false` | Register the Tavily source |
| `APIKey` | — | Tavily API key; overrides env |
| `APIKeyEnv` | `TAVILY_API_KEY` | Env var for the key |

Tavily is an [AI search API](https://tavily.com) that returns ranked results with
content snippets. The source is opt-in: it is only registered when a key resolves
and `Enabled` is not explicitly false. There is no HTML fallback.

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
