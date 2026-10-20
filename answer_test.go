package searchwire

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func answerJSONResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func clearAnswerProviderKeys(t *testing.T) {
	t.Helper()
	t.Setenv("BRAVE_SEARCH_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENAI", "")
	t.Setenv("PERPLEXITY_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("XAI_API_KEY", "")
	t.Setenv("OMNI_API_KEY", "")
	t.Setenv("OMNI_BASE_URL", "")
	t.Setenv("OMNI_SEARCH_PROVIDER", "")
	t.Setenv("OPENCODE_API_KEY", "")
	t.Setenv("OPENCODE2_API_KEY", "")
}

func TestAnswerUnavailableWithoutAPIKey(t *testing.T) {
	clearAnswerProviderKeys(t)
	searcher := New(Config{})
	if searcher.CanAnswer() {
		t.Fatal("CanAnswer returned true without an API key")
	}
	if _, err := searcher.Answer(context.Background(), "What is Go?"); !errors.Is(err, ErrAnswerUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestAvailableAnswerProvidersAreStableAndAnswerCanSelectOne(t *testing.T) {
	clearAnswerProviderKeys(t)
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Host {
		case "api.search.brave.com":
			return answerJSONResponse(`{"choices":[{"message":{"content":"from brave"}}]}`), nil
		case "openrouter.ai":
			return answerJSONResponse(`{"choices":[{"message":{"content":"from openrouter"}}]}`), nil
		case "api.openai.com":
			return answerJSONResponse(`{"output":[{"type":"message","content":[{"type":"output_text","text":"from openai"}]}]}`), nil
		default:
			t.Fatalf("unexpected host %q", r.URL.Host)
			return nil, nil
		}
	})}
	searcher := New(Config{
		HTTPClient: client,
		Brave:      BraveConfig{APIKey: "brave_test_key"},
		OpenRouter: OpenRouterConfig{APIKey: "openrouter_test_key"},
		OpenAI:     OpenAIConfig{APIKey: "openai_test_key"},
	})

	providers := searcher.AvailableAnswerProviders()
	want := []string{"brave", "openrouter", "openai"}
	if len(providers) != len(want) {
		t.Fatalf("providers = %#v", providers)
	}
	for i := range want {
		if providers[i] != want[i] {
			t.Fatalf("providers = %#v", providers)
		}
	}

	defaultAnswer, err := searcher.Answer(context.Background(), "question")
	if err != nil || defaultAnswer.Provider != "brave" {
		t.Fatalf("default answer = %#v, err = %v", defaultAnswer, err)
	}
	selected, err := searcher.AnswerWithOptions(context.Background(), "question", AnswerOptions{Provider: "openai"})
	if err != nil || selected.Provider != "openai" || selected.Text != "from openai" {
		t.Fatalf("selected answer = %#v, err = %v", selected, err)
	}
}

func TestAnswerRejectsUnavailableRequestedProvider(t *testing.T) {
	clearAnswerProviderKeys(t)
	searcher := New(Config{Brave: BraveConfig{APIKey: "brave_test_key"}})
	_, err := searcher.AnswerWithOptions(context.Background(), "question", AnswerOptions{Provider: "openai"})
	if !errors.Is(err, ErrAnswerProviderUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenRouterAnswerUsesServerWebSearchTool(t *testing.T) {
	t.Setenv("BRAVE_SEARCH_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENAI", "")
	var requestBody struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Type string `json:"type"`
		} `json:"tools"`
		MaxToolCalls int `json:"max_tool_calls"`
	}
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		if r.Method != http.MethodPost || r.URL.String() != "https://openrouter.ai/api/v1/chat/completions" {
			t.Fatalf("request = %s %s", r.Method, r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer openrouter_test_key" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		return answerJSONResponse(`{"choices":[{"message":{"content":"grounded answer"}}]}`), nil
	})}
	searcher := New(Config{HTTPClient: client, OpenRouter: OpenRouterConfig{APIKey: "openrouter_test_key"}})

	answer, err := searcher.Answer(context.Background(), "latest news")
	if err != nil {
		t.Fatal(err)
	}
	if requestBody.Model != "openrouter/auto" || len(requestBody.Messages) != 1 || requestBody.Messages[0].Content != "latest news" {
		t.Fatalf("request body = %#v", requestBody)
	}
	if len(requestBody.Tools) != 1 || requestBody.Tools[0].Type != "openrouter:web_search" || requestBody.MaxToolCalls != 1 {
		t.Fatalf("tools = %#v max_tool_calls = %d", requestBody.Tools, requestBody.MaxToolCalls)
	}
	if answer.Provider != "openrouter" || answer.Text != "grounded answer" {
		t.Fatalf("answer = %#v", answer)
	}
}

func TestOpenAIAnswerUsesResponsesWebSearchTool(t *testing.T) {
	t.Setenv("BRAVE_SEARCH_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	var requestBody struct {
		Model string `json:"model"`
		Input string `json:"input"`
		Tools []struct {
			Type string `json:"type"`
		} `json:"tools"`
	}
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		if r.Method != http.MethodPost || r.URL.String() != "https://api.openai.com/v1/responses" {
			t.Fatalf("request = %s %s", r.Method, r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer openai_test_key" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		return answerJSONResponse(`{"output":[{"type":"web_search_call"},{"type":"message","content":[{"type":"output_text","text":"grounded answer"}]}]}`), nil
	})}
	searcher := New(Config{HTTPClient: client, OpenAI: OpenAIConfig{APIKey: "openai_test_key"}})

	answer, err := searcher.Answer(context.Background(), "latest news")
	if err != nil {
		t.Fatal(err)
	}
	if requestBody.Model != "gpt-5.6-luna" || requestBody.Input != "latest news" || len(requestBody.Tools) != 1 || requestBody.Tools[0].Type != "web_search" {
		t.Fatalf("request body = %#v", requestBody)
	}
	if answer.Provider != "openai" || answer.Text != "grounded answer" {
		t.Fatalf("answer = %#v", answer)
	}
}

func TestPerplexityAnswerUsesLowCostAgentPreset(t *testing.T) {
	t.Setenv("BRAVE_SEARCH_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENAI", "")
	var requestBody struct {
		Preset string `json:"preset"`
		Input  string `json:"input"`
	}
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		if r.Method != http.MethodPost || r.URL.String() != "https://api.perplexity.ai/v1/agent" {
			t.Fatalf("request = %s %s", r.Method, r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer perplexity_test_key" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		return answerJSONResponse(`{"output":[{"type":"search_results","results":[]},{"type":"message","content":[{"type":"output_text","text":"grounded answer"}]}]}`), nil
	})}
	searcher := New(Config{HTTPClient: client, Perplexity: PerplexityConfig{APIKey: "perplexity_test_key"}})

	answer, err := searcher.Answer(context.Background(), "latest news")
	if err != nil {
		t.Fatal(err)
	}
	if requestBody.Preset != "low" || requestBody.Input != "latest news" {
		t.Fatalf("request body = %#v", requestBody)
	}
	if answer.Provider != "perplexity" || answer.Text != "grounded answer" {
		t.Fatalf("answer = %#v", answer)
	}
}

func TestAnthropicAnswerUsesHostedSearchAndPreservesCitations(t *testing.T) {
	clearAnswerProviderKeys(t)
	var requestBody struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		Messages  []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Type    string `json:"type"`
			Name    string `json:"name"`
			MaxUses int    `json:"max_uses"`
		} `json:"tools"`
	}
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		if r.Method != http.MethodPost || r.URL.String() != "https://api.anthropic.com/v1/messages" {
			t.Fatalf("request = %s %s", r.Method, r.URL)
		}
		if r.Header.Get("X-Api-Key") != "anthropic_test_key" || r.Header.Get("Anthropic-Version") != "2023-06-01" {
			t.Fatalf("headers = %#v", r.Header)
		}
		return answerJSONResponse(`{"stop_reason":"end_turn","content":[{"type":"text","text":"grounded answer","citations":[{"type":"web_search_result_location","url":"https://example.com/source","title":"Example source"},{"type":"web_search_result_location","url":"javascript:alert(1)","title":"Unsafe"}]}]}`), nil
	})}
	searcher := New(Config{HTTPClient: client, Anthropic: AnthropicConfig{APIKey: "anthropic_test_key"}})

	answer, err := searcher.Answer(context.Background(), "latest news")
	if err != nil {
		t.Fatal(err)
	}
	if requestBody.Model != "claude-haiku-4-5" || requestBody.MaxTokens != 2048 || len(requestBody.Messages) != 1 || requestBody.Messages[0].Content != "latest news" {
		t.Fatalf("request body = %#v", requestBody)
	}
	if len(requestBody.Tools) != 1 || requestBody.Tools[0].Type != "web_search_20250305" || requestBody.Tools[0].Name != "web_search" || requestBody.Tools[0].MaxUses != 1 {
		t.Fatalf("tools = %#v", requestBody.Tools)
	}
	if answer.Provider != "anthropic" || !strings.Contains(answer.Text, "grounded answer") || !strings.Contains(answer.Text, "[Example source](https://example.com/source)") {
		t.Fatalf("answer = %#v", answer)
	}
	if strings.Contains(answer.Text, "javascript:") {
		t.Fatalf("unsafe citation retained: %#v", answer)
	}
}

func TestXAIAnswerUsesResponsesWebSearchWithInlineCitations(t *testing.T) {
	clearAnswerProviderKeys(t)
	var requestBody struct {
		Model string `json:"model"`
		Input []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"input"`
		Tools []struct {
			Type string `json:"type"`
		} `json:"tools"`
	}
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		if r.Method != http.MethodPost || r.URL.String() != "https://api.x.ai/v1/responses" {
			t.Fatalf("request = %s %s", r.Method, r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer xai_test_key" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		return answerJSONResponse(`{"output":[{"type":"web_search_call"},{"type":"message","content":[{"type":"output_text","text":"grounded [[1]](https://example.com)"}]}]}`), nil
	})}
	searcher := New(Config{HTTPClient: client, XAI: XAIConfig{APIKey: "xai_test_key"}})

	answer, err := searcher.Answer(context.Background(), "latest news")
	if err != nil {
		t.Fatal(err)
	}
	if requestBody.Model != "grok-4.5" || len(requestBody.Input) != 1 || requestBody.Input[0].Content != "latest news" || len(requestBody.Tools) != 1 || requestBody.Tools[0].Type != "web_search" {
		t.Fatalf("request body = %#v", requestBody)
	}
	if answer.Provider != "xai" || !strings.Contains(answer.Text, "[[1]](https://example.com)") {
		t.Fatalf("answer = %#v", answer)
	}
}

func TestLLMAnswerProvidersNeverExposeAPIKeys(t *testing.T) {
	for name, testCase := range map[string]struct {
		provider string
		key      string
		config   func(HTTPClient) Config
	}{
		"openrouter": {
			provider: "openrouter",
			key:      "openrouter_secret_key",
			config: func(client HTTPClient) Config {
				return Config{HTTPClient: client, OpenRouter: OpenRouterConfig{APIKey: "openrouter_secret_key"}}
			},
		},
		"openai": {
			provider: "openai",
			key:      "openai_secret_key",
			config: func(client HTTPClient) Config {
				return Config{HTTPClient: client, OpenAI: OpenAIConfig{APIKey: "openai_secret_key"}}
			},
		},
		"perplexity": {
			provider: "perplexity",
			key:      "perplexity_secret_key",
			config: func(client HTTPClient) Config {
				return Config{HTTPClient: client, Perplexity: PerplexityConfig{APIKey: "perplexity_secret_key"}}
			},
		},
		"anthropic": {
			provider: "anthropic",
			key:      "anthropic_secret_key",
			config: func(client HTTPClient) Config {
				return Config{HTTPClient: client, Anthropic: AnthropicConfig{APIKey: "anthropic_secret_key"}}
			},
		},
		"xai": {
			provider: "xai",
			key:      "xai_secret_key",
			config: func(client HTTPClient) Config {
				return Config{HTTPClient: client, XAI: XAIConfig{APIKey: "xai_secret_key"}}
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusUnauthorized,
					Status:     "401 Unauthorized",
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"error":"invalid key ` + testCase.key + `"}`)),
				}, nil
			})}
			_, err := New(testCase.config(client)).AnswerWithOptions(
				context.Background(), "question", AnswerOptions{Provider: testCase.provider},
			)
			if err == nil || strings.Contains(err.Error(), testCase.key) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestAnswerUsesBraveBlockingEndpoint(t *testing.T) {
	var request *http.Request
	var requestBody braveAnswerRequest
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		request = r
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(readFixtureString(t, "brave-answer.json"))),
		}, nil
	})}
	searcher := New(Config{
		HTTPClient: client,
		Brave:      BraveConfig{APIKey: "brave_test_key"},
	})

	if !searcher.CanAnswer() {
		t.Fatal("CanAnswer returned false with an API key")
	}
	answer, err := searcher.Answer(context.Background(), "  What is Go?  ")
	if err != nil {
		t.Fatal(err)
	}
	if request.Method != http.MethodPost || request.URL.Host != "api.search.brave.com" || request.URL.Path != "/res/v1/chat/completions" {
		t.Fatalf("request URL = %s %s", request.Method, request.URL)
	}
	if request.Header.Get("X-Subscription-Token") != "brave_test_key" || request.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("headers = %#v", request.Header)
	}
	if requestBody.Model != "brave" || requestBody.Stream || len(requestBody.Messages) != 1 {
		t.Fatalf("request body = %#v", requestBody)
	}
	if requestBody.Messages[0].Role != "user" || requestBody.Messages[0].Content != "What is Go?" {
		t.Fatalf("message = %#v", requestBody.Messages[0])
	}
	if answer.Question != "What is Go?" || answer.Text != "Go is an open source programming language." || answer.Provider != "brave" {
		t.Fatalf("answer = %#v", answer)
	}
}

func TestAnswerRejectsEmptyQuestion(t *testing.T) {
	searcher := New(Config{Brave: BraveConfig{APIKey: "brave_test_key"}})
	if _, err := searcher.Answer(context.Background(), "   "); !errors.Is(err, ErrEmptyQuestion) {
		t.Fatalf("err = %v", err)
	}
}

func TestAnswerRejectsEmptyProviderResponse(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"choices":[]}`)),
		}, nil
	})}
	searcher := New(Config{HTTPClient: client, Brave: BraveConfig{APIKey: "brave_test_key"}})
	if _, err := searcher.Answer(context.Background(), "What is Go?"); !errors.Is(err, ErrInvalidProviderResponse) {
		t.Fatalf("err = %v", err)
	}
}

func TestAnswerNeverExposesAPIKeyInErrors(t *testing.T) {
	const apiKey = "brave_answer_secret_key"
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Status:     "401 Unauthorized",
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":"invalid key ` + apiKey + `"}`)),
		}, nil
	})}
	searcher := New(Config{HTTPClient: client, Brave: BraveConfig{APIKey: apiKey}})
	_, err := searcher.Answer(context.Background(), "What is Go?")
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), apiKey) {
		t.Fatalf("api key leaked in error: %v", err)
	}
}

func TestAnswerNeverExposesAPIKeyInSuccessfulResponse(t *testing.T) {
	const apiKey = "brave_answer_secret_key"
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(
				`{"choices":[{"message":{"role":"assistant","content":"key: ` + apiKey + `"}}]}`,
			)),
		}, nil
	})}
	searcher := New(Config{HTTPClient: client, Brave: BraveConfig{APIKey: apiKey}})
	answer, err := searcher.Answer(context.Background(), "What is Go?")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(answer.Text, apiKey) || !strings.Contains(answer.Text, "[redacted]") {
		t.Fatalf("answer = %#v", answer)
	}
}

func TestNilSearcherAnswerReturnsError(t *testing.T) {
	var searcher *Searcher
	if _, err := searcher.Answer(context.Background(), "What is Go?"); !errors.Is(err, ErrNilSearcher) {
		t.Fatalf("err = %v", err)
	}
}

func TestOmniRouteAnswerUsesRouterSearchEndpoint(t *testing.T) {
	clearAnswerProviderKeys(t)
	var requestBody struct {
		Query      string `json:"query"`
		Provider   string `json:"provider"`
		MaxResults int    `json:"max_results"`
	}
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		if r.Method != http.MethodPost || r.URL.String() != "http://router.local:20128/v1/search" {
			t.Fatalf("request = %s %s", r.Method, r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer omni_test_key" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		return answerJSONResponse(`{"provider":"tavily","results":[],"answer":{"source":"tavily","text":"synthesized answer","model":"tavily"}}`), nil
	})}
	searcher := New(Config{
		HTTPClient: client,
		OmniRoute:  OmniRouteConfig{APIKey: "omni_test_key", BaseURL: "http://router.local:20128/", Provider: "tavily"},
	})

	answer, err := searcher.Answer(context.Background(), "latest news")
	if err != nil {
		t.Fatal(err)
	}
	if requestBody.Query != "latest news" || requestBody.Provider != "tavily" || requestBody.MaxResults != omniRouteSearchLimit {
		t.Fatalf("request body = %#v", requestBody)
	}
	if answer.Provider != "omniroute" || answer.Text != "synthesized answer" {
		t.Fatalf("answer = %#v", answer)
	}
}

func TestOmniRouteAnswerComposesResultsWhenAnswerMissing(t *testing.T) {
	clearAnswerProviderKeys(t)
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "http://localhost:20128/v1/search" {
			t.Fatalf("request = %s %s", r.Method, r.URL)
		}
		return answerJSONResponse(`{"provider":"brave-search","answer":null,"results":[{"title":"Go","url":"https://go.dev","snippet":"The Go site"},{"title":"Gopher","url":"https://example.com/g","snippet":"Mascot"}]}`), nil
	})}
	searcher := New(Config{HTTPClient: client, OmniRoute: OmniRouteConfig{APIKey: "omni_test_key"}})

	answer, err := searcher.Answer(context.Background(), "latest news")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(answer.Text, "Go (https://go.dev)") || !strings.Contains(answer.Text, "Mascot") {
		t.Fatalf("answer = %#v", answer)
	}
}

func TestOmniRouteAnswerReadsEnvConfig(t *testing.T) {
	clearAnswerProviderKeys(t)
	t.Setenv("OMNI_API_KEY", "omni_env_key")
	t.Setenv("OMNI_BASE_URL", "http://router.local:4000")
	t.Setenv("OMNI_SEARCH_PROVIDER", "duckduckgo")
	var requestBody struct {
		Provider string `json:"provider"`
	}
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		if r.URL.String() != "http://router.local:4000/v1/search" {
			t.Fatalf("request = %s %s", r.Method, r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer omni_env_key" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		return answerJSONResponse(`{"answer":{"text":"env answer"},"results":[]}`), nil
	})}
	searcher := New(Config{HTTPClient: client})

	answer, err := searcher.Answer(context.Background(), "latest news")
	if err != nil {
		t.Fatal(err)
	}
	if requestBody.Provider != "duckduckgo" || answer.Provider != "omniroute" || answer.Text != "env answer" {
		t.Fatalf("answer = %#v provider = %q", answer, requestBody.Provider)
	}
}

func TestOpenCodeAnswerUsesZenResponsesWebSearchTool(t *testing.T) {
	clearAnswerProviderKeys(t)
	var requestBody struct {
		Model string `json:"model"`
		Input string `json:"input"`
		Tools []struct {
			Type string `json:"type"`
		} `json:"tools"`
	}
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		if r.Method != http.MethodPost || r.URL.String() != "https://opencode.ai/zen/v1/responses" {
			t.Fatalf("request = %s %s", r.Method, r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer opencode_test_key" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		return answerJSONResponse(`{"output":[{"type":"web_search_call"},{"type":"message","content":[{"type":"output_text","text":"zen answer"}]}]}`), nil
	})}
	searcher := New(Config{HTTPClient: client, OpenCode: OpenCodeConfig{APIKey: "opencode_test_key"}})

	answer, err := searcher.Answer(context.Background(), "latest news")
	if err != nil {
		t.Fatal(err)
	}
	if requestBody.Model != "gpt-5.6-luna" || requestBody.Input != "latest news" || len(requestBody.Tools) != 1 || requestBody.Tools[0].Type != "web_search" {
		t.Fatalf("request body = %#v", requestBody)
	}
	if answer.Provider != "opencode" || answer.Text != "zen answer" {
		t.Fatalf("answer = %#v", answer)
	}
}

func TestOpenCodeAnswerHonorsGoLaneBaseURL(t *testing.T) {
	clearAnswerProviderKeys(t)
	t.Setenv("OPENCODE2_API_KEY", "opencode2_env_key")
	t.Setenv("OPENCODE_BASE_URL", "https://opencode.ai/zen/go/v1")
	var sawURL string
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		sawURL = r.URL.String()
		return answerJSONResponse(`{"output":[{"type":"web_search_call"},{"type":"message","content":[{"type":"output_text","text":"go answer"}]}]}`), nil
	})}
	searcher := New(Config{HTTPClient: client})
	answer, err := searcher.AnswerWithOptions(context.Background(), "q", AnswerOptions{Provider: "opencode"})
	if err != nil {
		t.Fatal(err)
	}
	if sawURL != "https://opencode.ai/zen/go/v1/responses" {
		t.Fatalf("request url = %s", sawURL)
	}
	if answer.Text != "go answer" {
		t.Fatalf("answer = %#v", answer)
	}
}

func TestOpenCodeAnswerReadsSecondaryEnvKey(t *testing.T) {
	clearAnswerProviderKeys(t)
	t.Setenv("OPENCODE2_API_KEY", "opencode2_env_key")
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer opencode2_env_key" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		return answerJSONResponse(`{"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`), nil
	})}
	searcher := New(Config{HTTPClient: client})

	if !searcher.CanAnswer() {
		t.Fatal("opencode provider not registered from OPENCODE2_API_KEY")
	}
	answer, err := searcher.AnswerWithOptions(context.Background(), "q", AnswerOptions{Provider: "opencode"})
	if err != nil || answer.Text != "ok" {
		t.Fatalf("answer = %#v err = %v", answer, err)
	}
}
