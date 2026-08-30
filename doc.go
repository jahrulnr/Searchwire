// Package searchwire is a zero-configuration Go metasearch and page-fetch
// runtime for agent tooling. Callers provide a search query and optional Config;
// built-in sources fan out concurrently, partial failures are reported in the
// response, and results are deduplicated and ranked. Built-in sources include
// Brave Search, Startpage, Wikipedia, GitHub, and the opt-in Serper.dev and
// Tavily API sources (registered only when their API keys resolve). Searcher.Fetch retrieves
// one http(s) URL as readable text, with per-call controls for byte budget,
// conditional GET, and Range requests; it also surfaces llmstxt.org v2
// discovery hints (the covering llms.txt file and markdown alternates) when a
// page advertises them, and on 2xx HTML responses auto-attaches the parsed
// covering llms.txt (Searcher.LLMsTxt / Searcher.LLMsTxtURL). Truncated
// responses are also materialized to Config.TempDir (default
// os.TempDir()/searchwire) and surfaced as Searcher.TempFile so the agent can
// read the rest with file_read. Searcher.FetchLLMsTxt remains available as a
// standalone probe for callers that want the parsed llms.txt without
// fetching the page first. Searcher.SearchWithOptions runs
// a search with per-call options such as a different result limit than
// Config.Limit. When a supported provider API key is
// configured, Searcher.Answer returns web-grounded answers and CanAnswer reports
// that optional capability. The same library powers the searchwire-mcp stdio
// adapter under cmd/searchwire-mcp, where the answer tool is registered only
// when that capability is available at startup.
package searchwire
