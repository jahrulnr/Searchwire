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

func (p xAIAnswerProvider) answer(ctx context.Context, f *fetcher, question string) (answerResult, error) {
	payload, err := json.Marshal(xAIAnswerRequest{
		Model: p.model,
		Input: []xAIAnswerMessage{{Role: "user", Content: question}},
		Tools: []openAIAnswerTool{{Type: "web_search"}},
	})
	if err != nil {
		return answerResult{}, fmt.Errorf("encode xai answer request: %w", err)
	}
	headers := http.Header{"Authorization": []string{"Bearer " + p.apiKey}}
	body, err := f.postJSON(ctx, xAIResponsesEndpoint, payload, headers)
	if err != nil {
		return answerResult{}, redactErrorSecret(err, p.apiKey)
	}
	answer, err := parseResponsesAnswer(body, AnswerProviderXAI)
	if err != nil {
		return answerResult{}, err
	}
	return answerResult{Text: strings.ReplaceAll(answer, p.apiKey, "[redacted]"), SearchStatus: responsesSearchStatus(body)}, nil
}
