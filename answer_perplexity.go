package searchwire

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const perplexityAgentEndpoint = "https://api.perplexity.ai/v1/agent"

type perplexityAnswerProvider struct {
	apiKey string
	preset string
}

func (perplexityAnswerProvider) name() string { return AnswerProviderPerplexity }

type perplexityAnswerRequest struct {
	Preset string `json:"preset"`
	Input  string `json:"input"`
}

type perplexityAnswerResponse struct {
	Output []struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
}

func (p perplexityAnswerProvider) answer(ctx context.Context, f *fetcher, question string) (string, error) {
	payload, err := json.Marshal(perplexityAnswerRequest{Preset: p.preset, Input: question})
	if err != nil {
		return "", fmt.Errorf("encode perplexity answer request: %w", err)
	}

	headers := http.Header{"Authorization": []string{"Bearer " + p.apiKey}}
	body, err := f.postJSON(ctx, perplexityAgentEndpoint, payload, headers)
	if err != nil {
		return "", redactErrorSecret(err, p.apiKey)
	}
	var response perplexityAnswerResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("%w: parse perplexity answer: %v", ErrInvalidProviderResponse, err)
	}
	parts := make([]string, 0, 1)
	for _, output := range response.Output {
		if output.Type != "message" {
			continue
		}
		for _, content := range output.Content {
			if content.Type == "output_text" && strings.TrimSpace(content.Text) != "" {
				parts = append(parts, strings.TrimSpace(content.Text))
			}
		}
	}
	answer := strings.TrimSpace(strings.Join(parts, "\n\n"))
	if answer == "" {
		return "", ErrInvalidProviderResponse
	}
	return strings.ReplaceAll(answer, p.apiKey, "[redacted]"), nil
}
