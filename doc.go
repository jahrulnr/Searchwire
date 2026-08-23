// Package searchwire is a zero-configuration Go metasearch and page-fetch
// runtime for agent tooling. Callers provide a search query and optional Config;
// built-in sources fan out concurrently, partial failures are reported in the
// response, and results are deduplicated and ranked. Searcher.Fetch retrieves
// one http(s) URL as readable text, with per-call controls for byte budget,
// conditional GET, and Range requests. Searcher.SearchWithOptions runs a
// search with per-call options such as a different result limit than
// Config.Limit. When a supported provider API key is
// configured, Searcher.Answer returns web-grounded answers and CanAnswer reports
// that optional capability. The same library powers the searchwire-mcp stdio
// adapter under cmd/searchwire-mcp, where the answer tool is registered only
// when that capability is available at startup.
package searchwire
