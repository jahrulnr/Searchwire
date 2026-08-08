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
	Content string `json:"content"`
}

type anthropicAnswerTool struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	MaxUses int    `json:"max_uses"`
}

type anthropicAnswerResponse struct {
	StopReason string `json:"stop_reason"`
	Content    []struct {
		Type      string `json:"type"`
		Text      string `json:"text"`
		Citations []struct {
			URL   string `json:"url"`
			Title string `json:"title"`
		} `json:"citations"`
	} `json:"content"`
}

func (p anthropicAnswerProvider) answer(ctx context.Context, f *fetcher, question string) (string, error) {
	payload, err := json.Marshal(anthropicAnswerRequest{
		Model:     p.model,
		MaxTokens: 2048,
		Messages:  []anthropicAnswerMessage{{Role: "user", Content: question}},
		Tools:     []anthropicAnswerTool{{Type: "web_search_20250305", Name: "web_search", MaxUses: 1}},
	})
	if err != nil {
		return "", fmt.Errorf("encode anthropic answer request: %w", err)
	}

	headers := http.Header{
		"X-Api-Key":         []string{p.apiKey},
		"Anthropic-Version": []string{"2023-06-01"},
	}
	body, err := f.postJSON(ctx, anthropicMessagesEndpoint, payload, headers)
	if err != nil {
		return "", redactErrorSecret(err, p.apiKey)
	}
	var response anthropicAnswerResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("%w: parse anthropic answer: %v", ErrInvalidProviderResponse, err)
	}
	if response.StopReason != "end_turn" {
		return "", ErrInvalidProviderResponse
	}
	parts := make([]string, 0, 2)
	sources := make([]string, 0, 4)
	seenSources := make(map[string]struct{})
	for _, content := range response.Content {
		if content.Type != "text" || strings.TrimSpace(content.Text) == "" {
			continue
		}
		parts = append(parts, strings.TrimSpace(content.Text))
		for _, citation := range content.Citations {
			url := strings.TrimSpace(citation.URL)
			if !isSafeResultURL(url) {
				continue
			}
			if _, exists := seenSources[url]; exists {
				continue
			}
			seenSources[url] = struct{}{}
			title := strings.TrimSpace(citation.Title)
			if title == "" {
				title = url
			}
			sources = append(sources, "- ["+escapeMarkdownLinkText(title)+"]("+url+")")
		}
	}
	if len(sources) > 0 {
		parts = append(parts, "Sources:\n"+strings.Join(sources, "\n"))
	}
	answer := strings.TrimSpace(strings.Join(parts, "\n\n"))
	if answer == "" {
		return "", ErrInvalidProviderResponse
	}
	return strings.ReplaceAll(answer, p.apiKey, "[redacted]"), nil
}

func escapeMarkdownLinkText(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "[", "\\[")
	return strings.ReplaceAll(value, "]", "\\]")
}
