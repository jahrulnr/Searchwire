package searchwire

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const anthropicMessagesEndpoint = "https://api.anthropic.com/v1/messages"

type anthropicAnswerProvider struct {
	apiKey string
	model  string
}

func (anthropicAnswerProvider) name() string { return AnswerProviderAnthropic }

type anthropicAnswerRequest struct {
	Model     string                   `json:"model"`
	MaxTokens int                      `json:"max_tokens"`
	Messages  []anthropicAnswerMessage `json:"messages"`
	Tools     []anthropicAnswerTool    `json:"tools"`
}

type anthropicAnswerMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type anthropicAnswerTool struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	MaxUses int    `json:"max_uses"`
}

type anthropicAnswerResponse struct {
	StopReason string          `json:"stop_reason"`
	Content    json.RawMessage `json:"content"`
}

type anthropicAnswerContent struct {
	Type      string          `json:"type"`
	Name      string          `json:"name"`
	Text      string          `json:"text"`
	Content   json.RawMessage `json:"content"`
	Citations []answerSource  `json:"citations"`
}

func (p anthropicAnswerProvider) answer(ctx context.Context, f *fetcher, question string) (answerResult, error) {
	request := anthropicAnswerRequest{
		Model:     p.model,
		MaxTokens: 2048,
		Messages:  []anthropicAnswerMessage{{Role: "user", Content: question}},
		Tools:     []anthropicAnswerTool{{Type: "web_search_20250305", Name: "web_search", MaxUses: 1}},
	}
	headers := http.Header{
		"X-Api-Key":         []string{p.apiKey},
		"Anthropic-Version": []string{"2023-06-01"},
	}
	var parts []string
	var sources []answerSource
	searchStatus := "not_observed"
	// Bound repeated server pauses; the original context controls cancellation.
	const maxContinuations = 3
	for continuation := 0; continuation <= maxContinuations; continuation++ {
		payload, err := json.Marshal(request)
		if err != nil {
			return answerResult{}, fmt.Errorf("encode anthropic answer request: %w", err)
		}
		body, err := f.postJSON(ctx, anthropicMessagesEndpoint, payload, headers)
		if err != nil {
			return answerResult{}, redactErrorSecret(err, p.apiKey)
		}
		var response anthropicAnswerResponse
		if err := json.Unmarshal(body, &response); err != nil {
			return answerResult{}, fmt.Errorf("%w: parse anthropic answer: %v", ErrInvalidProviderResponse, err)
		}
		var content []anthropicAnswerContent
		if err := json.Unmarshal(response.Content, &content); err != nil {
			return answerResult{}, fmt.Errorf("%w: parse anthropic content: %v", ErrInvalidProviderResponse, err)
		}
		for _, block := range content {
			if block.Type == "server_tool_use" && block.Name == "web_search" {
				searchStatus = "observed"
			}
			if block.Type == "web_search_tool_result" {
				var result struct {
					Type      string `json:"type"`
					ErrorCode string `json:"error_code"`
				}
				if json.Unmarshal(block.Content, &result) == nil && result.Type == "web_search_tool_result_error" {
					return answerResult{}, fmt.Errorf("%w: anthropic web search failed: %s", ErrInvalidProviderResponse, strings.ReplaceAll(result.ErrorCode, p.apiKey, "[redacted]"))
				}
			}
			if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
				parts = append(parts, strings.TrimSpace(block.Text))
				sources = append(sources, block.Citations...)
			}
		}
		switch response.StopReason {
		case "end_turn":
			answer := strings.TrimSpace(strings.Join(parts, "\n\n"))
			if answer == "" {
				return answerResult{}, ErrInvalidProviderResponse
			}
			return answerResult{Text: strings.ReplaceAll(appendAnswerSources(answer, "Sources", sources), p.apiKey, "[redacted]"), SearchStatus: searchStatus}, nil
		case "pause_turn":
			if continuation == maxContinuations {
				return answerResult{}, fmt.Errorf("%w: anthropic continuation limit reached", ErrInvalidProviderResponse)
			}
			// Preserve all original blocks, including encrypted search data.
			request.Messages = append(request.Messages, anthropicAnswerMessage{Role: "assistant", Content: response.Content})
		default:
			return answerResult{}, fmt.Errorf("%w: anthropic stop reason %s", ErrInvalidProviderResponse, response.StopReason)
		}
	}
	return answerResult{}, ErrInvalidProviderResponse
}

func escapeMarkdownLinkText(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "[", "\\[")
	return strings.ReplaceAll(value, "]", "\\]")
}
