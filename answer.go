package searchwire

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const braveAnswersEndpoint = "https://api.search.brave.com/res/v1/chat/completions"

const (
	AnswerProviderBrave      = "brave"
	AnswerProviderOpenRouter = "openrouter"
	AnswerProviderOpenAI     = "openai"
	AnswerProviderPerplexity = "perplexity"
	AnswerProviderAnthropic  = "anthropic"
)

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

// AnswerOptions selects an optional configured provider. An empty provider
// uses the first available provider in Searchwire's stable priority order.
type AnswerOptions struct {
	Provider string
}

// CanAnswer reports whether this Searcher has a configured answer provider.
func (s *Searcher) CanAnswer() bool {
	return s != nil && len(s.answerers) > 0
}

// AvailableAnswerProviders returns configured provider identifiers in stable
// default-selection order.
func (s *Searcher) AvailableAnswerProviders() []string {
	if s == nil || len(s.answerers) == 0 {
		return nil
	}
	providers := make([]string, len(s.answerers))
	for i, provider := range s.answerers {
		providers[i] = provider.name()
	}
	return providers
}

// Answer returns a web-grounded answer when an answer provider is configured.
func (s *Searcher) Answer(ctx context.Context, question string) (*Answer, error) {
	return s.AnswerWithOptions(ctx, question, AnswerOptions{})
}

// AnswerWithOptions returns a web-grounded answer from the selected provider.
func (s *Searcher) AnswerWithOptions(ctx context.Context, question string, options AnswerOptions) (*Answer, error) {
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
	if len(s.answerers) == 0 {
		return nil, ErrAnswerUnavailable
	}
	provider := s.answerers[0]
	if requested := strings.ToLower(strings.TrimSpace(options.Provider)); requested != "" {
		provider = nil
		for _, candidate := range s.answerers {
			if candidate.name() == requested {
				provider = candidate
				break
			}
		}
		if provider == nil {
			return nil, fmt.Errorf("%w: %s", ErrAnswerProviderUnavailable, requested)
		}
	}

	fetcher := newFetcher(s.httpClient, s.userAgent, s.maxResponseSize)
	text, err := provider.answer(ctx, fetcher, question)
	if err != nil {
		return nil, err
	}
	return &Answer{Question: question, Text: text, Provider: provider.name()}, nil
}

func answerProvidersFromConfig(cfg Config) []answerProvider {
	providers := make([]answerProvider, 0, 5)
	if settings := cfg.Brave.resolved(); settings.apiKey != "" {
		providers = append(providers, braveAnswerProvider{apiKey: settings.apiKey})
	}
	if settings := cfg.OpenRouter.resolved(); settings.apiKey != "" {
		providers = append(providers, openRouterAnswerProvider{apiKey: settings.apiKey, model: settings.model})
	}
	if settings := cfg.OpenAI.resolved(); settings.apiKey != "" {
		providers = append(providers, openAIAnswerProvider{apiKey: settings.apiKey, model: settings.model})
	}
	if settings := cfg.Perplexity.resolved(); settings.apiKey != "" {
		providers = append(providers, perplexityAnswerProvider{apiKey: settings.apiKey, preset: settings.preset})
	}
	if settings := cfg.Anthropic.resolved(); settings.apiKey != "" {
		providers = append(providers, anthropicAnswerProvider{apiKey: settings.apiKey, model: settings.model})
	}
	return providers
}

type braveAnswerProvider struct {
	apiKey string
}

func (braveAnswerProvider) name() string { return AnswerProviderBrave }

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
