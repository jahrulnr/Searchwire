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
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content     string `json:"content"`
			Annotations []struct {
				Type        string       `json:"type"`
				URLCitation answerSource `json:"url_citation"`
			} `json:"annotations"`
		} `json:"message"`
	} `json:"choices"`
}

func (p openRouterAnswerProvider) answer(ctx context.Context, f *fetcher, question string) (answerResult, error) {
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
		return answerResult{}, fmt.Errorf("encode openrouter answer request: %w", err)
	}

	headers := http.Header{"Authorization": []string{"Bearer " + p.apiKey}}
	body, err := f.postJSON(ctx, openRouterChatCompletionsEndpoint, payload, headers)
	if err != nil {
		return answerResult{}, redactErrorSecret(err, p.apiKey)
	}
	var response openRouterAnswerResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return answerResult{}, fmt.Errorf("%w: parse openrouter answer: %v", ErrInvalidProviderResponse, err)
	}
	if len(response.Choices) == 0 {
		return answerResult{}, ErrInvalidProviderResponse
	}
	if reason := response.Choices[0].FinishReason; reason != "" && reason != "stop" {
		return answerResult{}, fmt.Errorf("%w: openrouter finish reason %s", ErrInvalidProviderResponse, reason)
	}
	answer := strings.TrimSpace(response.Choices[0].Message.Content)
	if answer == "" {
		return answerResult{}, ErrInvalidProviderResponse
	}
	var sources []answerSource
	for _, annotation := range response.Choices[0].Message.Annotations {
		if annotation.Type == "url_citation" {
			sources = append(sources, annotation.URLCitation)
		}
	}
	return answerResult{Text: strings.ReplaceAll(appendAnswerSources(answer, "Sources", sources), p.apiKey, "[redacted]"), SearchStatus: openRouterSearchStatus(body)}, nil
}

func openRouterSearchStatus(body []byte) string {
	var response struct {
		Usage struct {
			ServerToolUse struct {
				WebSearchRequests *int `json:"web_search_requests"`
			} `json:"server_tool_use"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &response) != nil || response.Usage.ServerToolUse.WebSearchRequests == nil {
		return "unknown"
	}
	if *response.Usage.ServerToolUse.WebSearchRequests > 0 {
		return "observed"
	}
	return "not_observed"
}
