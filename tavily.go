package searchwire

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const tavilyEndpoint = "https://api.tavily.com/search"

type tavilySource struct {
	apiKey string
}

func (tavilySource) name() string { return "tavily" }

func (s tavilySource) search(ctx context.Context, f *fetcher, query string, limit int) ([]sourceResult, error) {
	if limit <= 0 {
		limit = defaultLimit
	}
	body, err := json.Marshal(map[string]any{
		"query":       query,
		"max_results": limit,
	})
	if err != nil {
		return nil, fmt.Errorf("build tavily request: %w", err)
	}
	headers := http.Header{"Authorization": []string{"Bearer " + s.apiKey}}
	resp, err := f.postJSON(ctx, tavilyEndpoint, body, headers)
	if err != nil {
		return nil, redactErrorSecret(err, s.apiKey)
	}
	return parseTavilyResults(resp, limit)
}

type tavilyResponse struct {
	Results []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Content string `json:"content"`
	} `json:"results"`
}

func parseTavilyResults(body []byte, limit int) ([]sourceResult, error) {
	var payload tavilyResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parse tavily response: %w", err)
	}
	results := make([]sourceResult, 0, min(limit, len(payload.Results)))
	for _, item := range payload.Results {
		if len(results) >= limit {
			break
		}
		title := collapseWhitespace(item.Title)
		resultURL := strings.TrimSpace(item.URL)
		if title == "" || !isSafeResultURL(resultURL) {
			continue
		}
		results = append(results, sourceResult{
			title:   title,
			url:     resultURL,
			snippet: collapseWhitespace(item.Content),
			rank:    len(results) + 1,
		})
	}
	return results, nil
}
