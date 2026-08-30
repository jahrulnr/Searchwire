package searchwire

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const serperEndpoint = "https://google.serper.dev/search"

type serperSource struct {
	apiKey string
}

func (serperSource) name() string { return "serper" }

func (s serperSource) search(ctx context.Context, f *fetcher, query string, limit int) ([]sourceResult, error) {
	if limit <= 0 {
		limit = defaultLimit
	}
	body, err := json.Marshal(map[string]any{
		"q":   query,
		"num": limit,
	})
	if err != nil {
		return nil, fmt.Errorf("build serper request: %w", err)
	}
	headers := http.Header{"X-API-KEY": []string{s.apiKey}}
	resp, err := f.postJSON(ctx, serperEndpoint, body, headers)
	if err != nil {
		return nil, redactErrorSecret(err, s.apiKey)
	}
	return parseSerperResults(resp, limit)
}

type serperResponse struct {
	Organic []struct {
		Title   string `json:"title"`
		Link    string `json:"link"`
		Snippet string `json:"snippet"`
	} `json:"organic"`
}

func parseSerperResults(body []byte, limit int) ([]sourceResult, error) {
	var payload serperResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parse serper response: %w", err)
	}
	results := make([]sourceResult, 0, min(limit, len(payload.Organic)))
	for _, item := range payload.Organic {
		if len(results) >= limit {
			break
		}
		title := collapseWhitespace(item.Title)
		resultURL := strings.TrimSpace(item.Link)
		if title == "" || !isSafeResultURL(resultURL) {
			continue
		}
		results = append(results, sourceResult{
			title:   title,
			url:     resultURL,
			snippet: collapseWhitespace(item.Snippet),
			rank:    len(results) + 1,
		})
	}
	return results, nil
}
