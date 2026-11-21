package searchwire

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// openCodeDefaultBaseURL is the OpenCode Zen lane; the Go subscription lane is
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

func (p openCodeAnswerProvider) answer(ctx context.Context, f *fetcher, question string) (answerResult, error) {
	payload, err := json.Marshal(openCodeAnswerRequest{
		Model: p.model,
		Input: question,
		Tools: []openCodeAnswerTool{{Type: "web_search"}},
	})
	if err != nil {
		return answerResult{}, fmt.Errorf("encode opencode answer request: %w", err)
	}

	var session [16]byte
	if _, err := rand.Read(session[:]); err != nil {
		return answerResult{}, fmt.Errorf("create opencode session: %w", err)
	}
	headers := http.Header{
		"Authorization":      []string{"Bearer " + p.apiKey},
		"X-Opencode-Session": []string{hex.EncodeToString(session[:])},
	}
	body, err := f.postJSON(ctx, strings.TrimRight(p.baseURL, "/")+"/responses", payload, headers)
	if err != nil {
		return answerResult{}, redactErrorSecret(err, p.apiKey)
	}
	answer, err := parseResponsesAnswer(body, AnswerProviderOpenCode)
	if err != nil {
		return answerResult{}, err
	}
	return answerResult{Text: strings.ReplaceAll(answer, p.apiKey, "[redacted]"), SearchStatus: responsesSearchStatus(body)}, nil
}
