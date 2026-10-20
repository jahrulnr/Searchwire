package searchwire

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// openCodeDefaultBaseURL is the OpenCode Zen paid lane; the free Go lane is
// https://opencode.ai/zen/go/v1 — both expose the same Responses API.
const openCodeDefaultBaseURL = "https://opencode.ai/zen/v1"

type openCodeAnswerProvider struct {
	apiKey  string
	model   string
	baseURL string
}

func (openCodeAnswerProvider) name() string { return AnswerProviderOpenCode }

type openCodeAnswerRequest struct {
	Model string               `json:"model"`
	Input string               `json:"input"`
	Tools []openCodeAnswerTool `json:"tools"`
}

type openCodeAnswerTool struct {
	Type string `json:"type"`
}

func (p openCodeAnswerProvider) answer(ctx context.Context, f *fetcher, question string) (string, error) {
	payload, err := json.Marshal(openCodeAnswerRequest{
		Model: p.model,
		Input: question,
		Tools: []openCodeAnswerTool{{Type: "web_search"}},
	})
	if err != nil {
		return "", fmt.Errorf("encode opencode answer request: %w", err)
	}

	headers := http.Header{"Authorization": []string{"Bearer " + p.apiKey}}
	body, err := f.postJSON(ctx, strings.TrimRight(p.baseURL, "/")+"/responses", payload, headers)
	if err != nil {
		return "", redactErrorSecret(err, p.apiKey)
	}
	answer, err := parseResponsesAnswer(body, AnswerProviderOpenCode)
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(answer, p.apiKey, "[redacted]"), nil
}
