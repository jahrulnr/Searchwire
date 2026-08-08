package searchwire

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const xAIResponsesEndpoint = "https://api.x.ai/v1/responses"

type xAIAnswerProvider struct {
	apiKey string
	model  string
}

func (xAIAnswerProvider) name() string { return AnswerProviderXAI }

type xAIAnswerRequest struct {
	Model string             `json:"model"`
	Input []xAIAnswerMessage `json:"input"`
	Tools []openAIAnswerTool `json:"tools"`
}

type xAIAnswerMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (p xAIAnswerProvider) answer(ctx context.Context, f *fetcher, question string) (string, error) {
	payload, err := json.Marshal(xAIAnswerRequest{
		Model: p.model,
		Input: []xAIAnswerMessage{{Role: "user", Content: question}},
		Tools: []openAIAnswerTool{{Type: "web_search"}},
	})
	if err != nil {
		return "", fmt.Errorf("encode xai answer request: %w", err)
	}
	headers := http.Header{"Authorization": []string{"Bearer " + p.apiKey}}
	body, err := f.postJSON(ctx, xAIResponsesEndpoint, payload, headers)
	if err != nil {
		return "", redactErrorSecret(err, p.apiKey)
	}
	var response openAIAnswerResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("%w: parse xai answer: %v", ErrInvalidProviderResponse, err)
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
