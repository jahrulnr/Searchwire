package searchwire

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const openAIResponsesEndpoint = "https://api.openai.com/v1/responses"

type openAIAnswerProvider struct {
	apiKey string
	model  string
}

func (openAIAnswerProvider) name() string { return AnswerProviderOpenAI }

type openAIAnswerRequest struct {
	Model string             `json:"model"`
	Input string             `json:"input"`
	Tools []openAIAnswerTool `json:"tools"`
}

type openAIAnswerTool struct {
	Type string `json:"type"`
}

type openAIAnswerResponse struct {
	Output []struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
}

func (p openAIAnswerProvider) answer(ctx context.Context, f *fetcher, question string) (string, error) {
	payload, err := json.Marshal(openAIAnswerRequest{
		Model: p.model,
		Input: question,
		Tools: []openAIAnswerTool{{Type: "web_search"}},
	})
	if err != nil {
		return "", fmt.Errorf("encode openai answer request: %w", err)
	}

	headers := http.Header{"Authorization": []string{"Bearer " + p.apiKey}}
	body, err := f.postJSON(ctx, openAIResponsesEndpoint, payload, headers)
	if err != nil {
		return "", redactErrorSecret(err, p.apiKey)
	}
	var response openAIAnswerResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("%w: parse openai answer: %v", ErrInvalidProviderResponse, err)
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
