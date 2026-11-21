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
	AnswerProviderXAI        = "xai"
	AnswerProviderOmniRoute  = "omniroute"
	AnswerProviderOpenCode   = "opencode"
)

type answerProvider interface {
	name() string
	answer(context.Context, *fetcher, string) (answerResult, error)
}

// answerResult keeps provider-specific execution metadata with its text.
type answerResult struct {
	Text         string
	SearchStatus string
}

// Answer is a response from an optional web-capable answer provider.
type Answer struct {
	Question string
	Text     string
	Provider string
	// SearchStatus is "observed", "not_observed", or "unknown". It reports
	// observable provider search activity, not a guarantee of answer quality.
	SearchStatus string
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

// Answer returns a response when an answer provider is configured.
func (s *Searcher) Answer(ctx context.Context, question string) (*Answer, error) {
	return s.AnswerWithOptions(ctx, question, AnswerOptions{})
}

// AnswerWithOptions returns a response from the selected provider.
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

	fetcher := newFetcher(s.answerHTTPClient, s.userAgent, s.maxResponseSize)
	result, err := provider.answer(ctx, fetcher, question)
	if err != nil {
		for _, secret := range s.secrets {
			err = redactErrorSecret(err, secret)
		}
		return nil, err
	}
	if result.SearchStatus == "" {
		result.SearchStatus = "unknown"
	}
	for _, secret := range s.secrets {
		result.Text = strings.ReplaceAll(result.Text, secret, "[redacted]")
	}
	return &Answer{Question: question, Text: result.Text, Provider: provider.name(), SearchStatus: result.SearchStatus}, nil
}

func answerProvidersFromConfig(cfg Config) []answerProvider {
	providers := make([]answerProvider, 0, 6)
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
	if settings := cfg.XAI.resolved(); settings.apiKey != "" {
		providers = append(providers, xAIAnswerProvider{apiKey: settings.apiKey, model: settings.model})
	}
	if settings := cfg.OmniRoute.resolved(); settings.apiKey != "" {
		providers = append(providers, omniRouteAnswerProvider{apiKey: settings.apiKey, baseURL: settings.baseURL, provider: settings.provider})
	}
	if settings := cfg.OpenCode.resolved(); settings.apiKey != "" {
		providers = append(providers, openCodeAnswerProvider{apiKey: settings.apiKey, model: settings.model, baseURL: settings.baseURL})
	}
	return providers
}

type braveAnswerProvider struct {
	apiKey string
}

func (braveAnswerProvider) name() string { return AnswerProviderBrave }

type braveAnswerRequest struct {
	Model           string               `json:"model"`
	Stream          bool                 `json:"stream"`
	EnableCitations bool                 `json:"enable_citations"`
	Messages        []braveAnswerMessage `json:"messages"`
}

type braveAnswerMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type braveAnswerResponse struct {
	Choices []struct {
		Message      braveAnswerMessage `json:"message"`
		FinishReason string             `json:"finish_reason"`
	} `json:"choices"`
}

func (b braveAnswerProvider) answer(ctx context.Context, f *fetcher, question string) (answerResult, error) {
	payload, err := json.Marshal(braveAnswerRequest{
		Model:           "brave",
		Stream:          true,
		EnableCitations: true,
		Messages: []braveAnswerMessage{{
			Role:    "user",
			Content: question,
		}},
	})
	if err != nil {
		return answerResult{}, fmt.Errorf("encode brave answer request: %w", err)
	}

	headers := http.Header{"X-Subscription-Token": []string{b.apiKey}}
	body, err := f.do(ctx, http.MethodPost, braveAnswersEndpoint, "text/event-stream, application/json", "application/json", strings.NewReader(string(payload)), []string{"text/event-stream", "application/json"}, headers)
	if err != nil {
		return answerResult{}, redactErrorSecret(err, b.apiKey)
	}
	if !json.Valid(body) {
		answer, err := parseBraveAnswerStream(body)
		if err != nil {
			return answerResult{}, err
		}
		return answerResult{Text: strings.ReplaceAll(answer, b.apiKey, "[redacted]")}, nil
	}
	var response braveAnswerResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return answerResult{}, fmt.Errorf("%w: parse brave answer: %v", ErrInvalidProviderResponse, err)
	}
	if len(response.Choices) == 0 {
		return answerResult{}, ErrInvalidProviderResponse
	}
	if reason := response.Choices[0].FinishReason; reason != "" && reason != "stop" {
		return answerResult{}, fmt.Errorf("%w: brave finish reason %s", ErrInvalidProviderResponse, reason)
	}
	answer := strings.TrimSpace(response.Choices[0].Message.Content)
	if answer == "" {
		return answerResult{}, ErrInvalidProviderResponse
	}
	return answerResult{Text: strings.ReplaceAll(answer, b.apiKey, "[redacted]")}, nil
}
