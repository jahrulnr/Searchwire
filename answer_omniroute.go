package searchwire

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// OmniRoute default endpoint is the locally installed gateway. Callers running
// the router elsewhere override it through OmniRouteConfig.BaseURL.
const omniRouteDefaultBaseURL = "http://localhost:20128"

// omniRouteSearchLimit caps how many results are rendered when the router
// returns no synthesized answer.
const omniRouteSearchLimit = 8

type omniRouteAnswerProvider struct {
	apiKey   string
	baseURL  string
	provider string
}

func (omniRouteAnswerProvider) name() string { return AnswerProviderOmniRoute }

type omniRouteSearchRequest struct {
	Query      string `json:"query"`
	Provider   string `json:"provider,omitempty"`
	MaxResults int    `json:"max_results,omitempty"`
}

type omniRouteSearchResponse struct {
	Answer  json.RawMessage `json:"answer"`
	Results []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Snippet string `json:"snippet"`
	} `json:"results"`
}

// omniRouteAnswerBody tolerates both the string form and the object form
// ({source, text, model}) produced by the router's chat-search lane.
type omniRouteAnswerBody struct {
	Text string `json:"text"`
}

func (p omniRouteAnswerProvider) answer(ctx context.Context, f *fetcher, question string) (string, error) {
	payload, err := json.Marshal(omniRouteSearchRequest{
		Query:      question,
		Provider:   p.provider,
		MaxResults: omniRouteSearchLimit,
	})
	if err != nil {
		return "", fmt.Errorf("encode omniroute search request: %w", err)
	}

	headers := http.Header{"Authorization": []string{"Bearer " + p.apiKey}}
	body, err := f.postJSON(ctx, strings.TrimRight(p.baseURL, "/")+"/v1/search", payload, headers)
	if err != nil {
		return "", redactErrorSecret(err, p.apiKey)
	}
	var response omniRouteSearchResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("%w: parse omniroute search: %v", ErrInvalidProviderResponse, err)
	}
	if answer := parseOmniRouteAnswer(response.Answer); answer != "" {
		return strings.ReplaceAll(answer, p.apiKey, "[redacted]"), nil
	}
	if len(response.Results) == 0 {
		return "", ErrInvalidProviderResponse
	}
	var b strings.Builder
	for i, result := range response.Results {
		if i >= omniRouteSearchLimit {
			break
		}
		if result.Title == "" && result.Snippet == "" {
			continue
		}
		fmt.Fprintf(&b, "- %s (%s)\n  %s\n", result.Title, result.URL, result.Snippet)
	}
	answer := strings.TrimSpace(b.String())
	if answer == "" {
		return "", ErrInvalidProviderResponse
	}
	return strings.ReplaceAll(answer, p.apiKey, "[redacted]"), nil
}

func parseOmniRouteAnswer(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var object omniRouteAnswerBody
	if err := json.Unmarshal(raw, &object); err == nil && object.Text != "" {
		return strings.TrimSpace(object.Text)
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.TrimSpace(text)
	}
	return ""
}
