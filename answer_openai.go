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

func (p openAIAnswerProvider) answer(ctx context.Context, f *fetcher, question string) (answerResult, error) {
	payload, err := json.Marshal(openAIAnswerRequest{
		Model: p.model,
		Input: question,
		Tools: []openAIAnswerTool{{Type: "web_search"}},
	})
	if err != nil {
		return answerResult{}, fmt.Errorf("encode openai answer request: %w", err)
	}

	headers := http.Header{"Authorization": []string{"Bearer " + p.apiKey}}
	body, err := f.postJSON(ctx, openAIResponsesEndpoint, payload, headers)
	if err != nil {
		return answerResult{}, redactErrorSecret(err, p.apiKey)
	}
	answer, err := parseResponsesAnswer(body, AnswerProviderOpenAI)
	if err != nil {
		return answerResult{}, err
	}
	return answerResult{Text: strings.ReplaceAll(answer, p.apiKey, "[redacted]"), SearchStatus: responsesSearchStatus(body)}, nil
}
