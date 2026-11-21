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

func (p perplexityAnswerProvider) answer(ctx context.Context, f *fetcher, question string) (answerResult, error) {
	payload, err := json.Marshal(perplexityAnswerRequest{Preset: p.preset, Input: question})
	if err != nil {
		return answerResult{}, fmt.Errorf("encode perplexity answer request: %w", err)
	}

	headers := http.Header{"Authorization": []string{"Bearer " + p.apiKey}}
	body, err := f.postJSON(ctx, perplexityAgentEndpoint, payload, headers)
	if err != nil {
		return answerResult{}, redactErrorSecret(err, p.apiKey)
	}
	answer, err := parseResponsesAnswer(body, AnswerProviderPerplexity)
	if err != nil {
		return answerResult{}, err
	}
	return answerResult{Text: strings.ReplaceAll(answer, p.apiKey, "[redacted]"), SearchStatus: responsesSearchStatus(body)}, nil
}
