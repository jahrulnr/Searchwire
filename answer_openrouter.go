package searchwire

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const openRouterChatCompletionsEndpoint = "https://openrouter.ai/api/v1/chat/completions"

type openRouterAnswerProvider struct {
	apiKey string
	model  string
}

func (openRouterAnswerProvider) name() string { return AnswerProviderOpenRouter }

type openRouterAnswerRequest struct {
	Model        string                    `json:"model"`
	Messages     []openRouterAnswerMessage `json:"messages"`
	Tools        []openRouterAnswerTool    `json:"tools"`
	MaxToolCalls int                       `json:"max_tool_calls"`
	MaxTokens    int                       `json:"max_tokens,omitempty"`
}

type openRouterAnswerMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openRouterAnswerTool struct {
	Type string `json:"type"`
}

type openRouterAnswerResponse struct {
	Choices []struct {
		Message openRouterAnswerMessage `json:"message"`
	} `json:"choices"`
}

func (p openRouterAnswerProvider) answer(ctx context.Context, f *fetcher, question string) (string, error) {
	payload, err := json.Marshal(openRouterAnswerRequest{
		Model: p.model,
		Messages: []openRouterAnswerMessage{{
			Role:    "user",
			Content: question,
		}},
		Tools:        []openRouterAnswerTool{{Type: "openrouter:web_search"}},
		MaxToolCalls: 1,
		MaxTokens:    2048,
	})
	if err != nil {
		return "", fmt.Errorf("encode openrouter answer request: %w", err)
	}

	headers := http.Header{"Authorization": []string{"Bearer " + p.apiKey}}
	body, err := f.postJSON(ctx, openRouterChatCompletionsEndpoint, payload, headers)
	if err != nil {
		return "", redactErrorSecret(err, p.apiKey)
	}
	var response openRouterAnswerResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("%w: parse openrouter answer: %v", ErrInvalidProviderResponse, err)
	}
	if len(response.Choices) == 0 {
		return "", ErrInvalidProviderResponse
	}
	answer := strings.TrimSpace(response.Choices[0].Message.Content)
	if answer == "" {
		return "", ErrInvalidProviderResponse
	}
	return strings.ReplaceAll(answer, p.apiKey, "[redacted]"), nil
}
