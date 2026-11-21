package searchwire

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

const hackerNewsItemURL = "https://news.ycombinator.com/item?id="

// hackerNewsSource queries the public HN Search API (Algolia) for stories.
// It needs no key. Results point at the linked article; the discussion URL
// and points/comment counts are carried in the snippet so a caller can
// judge community signal and follow the thread.
type hackerNewsSource struct{}

func (hackerNewsSource) name() string { return "hackernews" }

func (hackerNewsSource) search(ctx context.Context, f *fetcher, query string, limit int) ([]sourceResult, error) {
	if limit <= 0 {
		limit = defaultLimit
	}
	endpoint := url.URL{Scheme: "https", Host: "hn.algolia.com", Path: "/api/v1/search"}
	params := endpoint.Query()
	params.Set("query", query)
	params.Set("tags", "story")
	// Over-fetch slightly: resubmissions of the same URL collapse.
	params.Set("hitsPerPage", strconv.Itoa(min(limit*2, 100)))
	endpoint.RawQuery = params.Encode()

	body, err := f.get(ctx, endpoint.String(), "application/json", []string{"application/json"})
	if err != nil {
		return nil, err
	}
	return parseHackerNewsResults(body, limit)
}

type hackerNewsResponse struct {
	Hits []struct {
		ObjectID    string `json:"objectID"`
		Title       string `json:"title"`
		URL         string `json:"url"`
		Points      int    `json:"points"`
		NumComments int    `json:"num_comments"`
		CreatedAt   string `json:"created_at"`
		StoryText   string `json:"story_text"`
	} `json:"hits"`
}

func parseHackerNewsResults(body []byte, limit int) ([]sourceResult, error) {
	var payload hackerNewsResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode hackernews response: %w", err)
	}
	seen := make(map[string]bool, len(payload.Hits))
	results := make([]sourceResult, 0, min(limit, len(payload.Hits)))
	for _, hit := range payload.Hits {
		if len(results) >= limit {
			break
		}
		title := collapseWhitespace(hit.Title)
		if title == "" || hit.ObjectID == "" {
			continue
		}
		discussion := hackerNewsItemURL + url.QueryEscape(hit.ObjectID)
		link := strings.TrimSpace(hit.URL)
		if !isSafeResultURL(link) {
			// Ask/Show HN text posts have no external URL.
			link = discussion
		}
		key, _, err := canonicalURL(link)
		if err != nil || seen[key] {
			continue
		}
		seen[key] = true

		meta := fmt.Sprintf("%d points · %d comments", hit.Points, hit.NumComments)
		if len(hit.CreatedAt) >= 10 {
			meta += " · " + hit.CreatedAt[:10]
		}
		if link != discussion {
			meta += " · discussion: " + discussion
		}
		snippet := "[" + meta + "]"
		if text := sanitizeSnippet(hit.StoryText); text != "" {
			snippet += " " + text
		}
		results = append(results, sourceResult{
			title:   title,
			url:     link,
			snippet: snippet,
			rank:    len(results) + 1,
		})
	}
	return results, nil
}
