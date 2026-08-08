package searchwire

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const braveAnswersEndpoint = "https://api.search.brave.com/res/v1/chat/completions"

type answerProvider interface {
	name() string
	answer(context.Context, *fetcher, string) (string, error)
}

// Answer is one web-grounded response from an optional answer provider.
type Answer struct {
	Question string
	Text     string
	Provider string
}

// CanAnswer reports whether this Searcher has a configured answer provider.
func (s *Searcher) CanAnswer() bool {
	return s != nil && s.answerer != nil
}

// Answer returns a web-grounded answer when an answer provider is configured.
func (s *Searcher) Answer(ctx context.Context, question string) (*Answer, error) {
	if s == nil {
		return nil, ErrNilSearcher
	}
	question = strings.TrimSpace(question)
	if question == "" {
		return nil, ErrEmptyQuestion
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.answerer == nil {
		return nil, ErrAnswerUnavailable
	}

	fetcher := newFetcher(s.httpClient, s.userAgent, s.maxResponseSize)
	text, err := s.answerer.answer(ctx, fetcher, question)
	if err != nil {
		return nil, err
	}
	return &Answer{Question: question, Text: text, Provider: s.answerer.name()}, nil
}

func answerProviderFromConfig(cfg Config) answerProvider {
	settings := cfg.Brave.resolved()
	if settings.apiKey == "" {
		return nil
	}
	return braveAnswerProvider{apiKey: settings.apiKey}
}

type braveAnswerProvider struct {
	apiKey string
}

func (braveAnswerProvider) name() string { return "brave" }

type braveAnswerRequest struct {
	Model    string               `json:"model"`
	Stream   bool                 `json:"stream"`
	Messages []braveAnswerMessage `json:"messages"`
}

type braveAnswerMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type braveAnswerResponse struct {
	Choices []struct {
		Message braveAnswerMessage `json:"message"`
	} `json:"choices"`
}

func (b braveAnswerProvider) answer(ctx context.Context, f *fetcher, question string) (string, error) {
	payload, err := json.Marshal(braveAnswerRequest{
		Model:  "brave",
		Stream: false,
		Messages: []braveAnswerMessage{{
			Role:    "user",
			Content: question,
		}},
	})
	if err != nil {
		return "", fmt.Errorf("encode brave answer request: %w", err)
	}

	headers := http.Header{"X-Subscription-Token": []string{b.apiKey}}
	body, err := f.postJSON(ctx, braveAnswersEndpoint, payload, headers)
	if err != nil {
		return "", redactErrorSecret(err, b.apiKey)
	}
	var response braveAnswerResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("%w: parse brave answer: %v", ErrInvalidProviderResponse, err)
	}
	if len(response.Choices) == 0 {
		return "", ErrInvalidProviderResponse
	}
	answer := strings.TrimSpace(response.Choices[0].Message.Content)
	if answer == "" {
		return "", ErrInvalidProviderResponse
	}
	return strings.ReplaceAll(answer, b.apiKey, "[redacted]"), nil
}
