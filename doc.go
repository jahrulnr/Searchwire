// Package searchwire is a zero-configuration Go metasearch and page-fetch
// runtime for agent tooling. Callers provide a search query and optional Config;
// built-in sources fan out concurrently, partial failures are reported in the
// response, and results are deduplicated and ranked. Searcher.Fetch retrieves
// one http(s) URL as readable text. The same library powers the searchwire-mcp
// stdio adapter under cmd/searchwire-mcp.
package searchwire
