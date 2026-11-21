package searchwire

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// These are minimal, synthetic examples based on the linked provider contracts,
// not captured provider responses. OpenCode hosted search was later observed
// in a live probe that injected the required session header outside Searchwire.
// See docs/web-answer-provider-audit.md for provenance and limitations.
func TestAnswerProviderResponseFixtures(t *testing.T) {
	cases := []struct {
		provider string
		body     string
		want     string
	}{
		// Brave Answers, non-streaming response:
		// https://api-dashboard.search.brave.com/documentation/services/answers
		{"brave", `{"choices":[{"message":{"role":"assistant","content":"A fact."}}]}`, "A fact."},
		// OpenRouter server-side web search and Chat Completions:
		// https://openrouter.ai/tool/web-search/
		{"openrouter", `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"A fact. [Source](https://example.com/source)"}}]}`, "https://example.com/source"},
		// OpenAI Responses web-search output:
		// https://developers.openai.com/api/docs/guides/tools-web-search
		{"openai", `{"status":"completed","output":[{"type":"web_search_call","status":"completed"},{"type":"message","content":[{"type":"output_text","text":"A fact."}]}]}`, "A fact."},
		// Perplexity Agent output and search_results item:
		// https://github.com/perplexityai/api-platform-developers/blob/main/skills/migrate-sonar-to-agent-api/references/response-and-streaming.md
		{"perplexity", `{"status":"completed","output":[{"type":"search_results","results":[{"id":"web:1","title":"Source","url":"https://example.com/source"}]},{"type":"message","content":[{"type":"output_text","text":"A fact [web:1]."}]}]}`, "A fact [web:1]."},
		// Anthropic server-tool blocks and web-search citations:
		// https://platform.claude.com/docs/en/agents-and-tools/tool-use/web-search-tool
		{"anthropic", `{"stop_reason":"end_turn","content":[{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"test"}},{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[]},{"type":"text","text":"A fact.","citations":[{"type":"web_search_result_location","title":"Source","url":"https://example.com/source"}]}]}`, "https://example.com/source"},
		// xAI Responses inline citation format:
		// https://docs.x.ai/developers/tools/citations
		{"xai", `{"status":"completed","output":[{"type":"web_search_call","status":"completed"},{"type":"message","content":[{"type":"output_text","text":"A fact. [[1]](https://example.com/source)"}]}]}`, "https://example.com/source"},
		// OmniRoute search response wrapper (upstream issue, not API reference):
		// https://github.com/diegosouzapw/OmniRoute/issues/10628
		// The object-form answer in this synthetic combination is not verified there.
		{"omniroute", `{"answer":{"text":"A fact.","source":"test","model":"test"},"results":[{"title":"Source","url":"https://example.com/source","snippet":"A fact."}]}`, "A fact."},
		// OpenCode Go hosted search was observed in a live probe with a session
		// header injected externally; this example is still synthetic:
		// https://opencode.ai/v2/docs/console/go/#where-can-i-use-it
		{"opencode", `{"status":"completed","output":[{"type":"web_search_call","status":"completed"},{"type":"message","content":[{"type":"output_text","text":"A fact."}]}]}`, "A fact."},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			answer, err := answerFixture(t, tc.provider, tc.body, http.StatusOK)
			if err != nil {
				t.Fatal(err)
			}
			if answer.Provider != tc.provider || !strings.Contains(answer.Text, tc.want) {
				t.Fatalf("answer = %#v; want provider %q and text containing %q", answer, tc.provider, tc.want)
			}
		})
	}
}

// OpenCode Go documented session requirement:
// https://opencode.ai/v2/docs/console/go/#where-can-i-use-it
// Synthetic reconstruction of a live 2026-10-31 HTTP 400: Searchwire's
// current request had no x-opencode-session and the gateway rejected it.
func TestOpenCodeGoMissingSessionResponse(t *testing.T) {
	body := `{"type":"error","error":{"type":"MissingSessionID","message":"Request is missing x-opencode-session and cannot be routed efficiently."}}`
	_, err := answerFixture(t, "opencode", body, http.StatusBadRequest)
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusBadRequest || !strings.Contains(err.Error(), "MissingSessionID") {
		t.Fatalf("got %v; want HTTP 400 with MissingSessionID", err)
	}
}

// A standalone Answer is one conversation; each call must carry its own
// session ID so OpenCode Go can route it:
// https://opencode.ai/v2/docs/console/go/#where-can-i-use-it
func TestOpenCodeGoSendsDistinctSessionForEachAnswer(t *testing.T) {
	clearAnswerProviderKeys(t)
	var sessions []string
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		sessions = append(sessions, r.Header.Get("x-opencode-session"))
		return answerJSONResponse(`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`), nil
	})}
	s := New(Config{HTTPClient: client, OpenCode: OpenCodeConfig{APIKey: "fixture-key", BaseURL: "https://opencode.ai/zen/go/v1"}})
	for range 2 {
		if _, err := s.AnswerWithOptions(context.Background(), "question", AnswerOptions{Provider: "opencode"}); err != nil {
			t.Fatal(err)
		}
	}
	if len(sessions) != 2 || sessions[0] == "" || sessions[1] == "" || sessions[0] == sessions[1] {
		t.Fatalf("session IDs = %#v; want two distinct nonempty IDs", sessions)
	}
}

// OpenCode Go session requirement and endpoints:
// https://opencode.ai/v2/docs/console/go/
// Synthetic reconstruction of a live 2026-10-31 response after the probe
// injected x-opencode-session: search, open_page, find_in_page, then text with
// annotations. This tests parsing only, not Searchwire's ability to send that
// session header; the answer and URLs below are placeholders, not a recording.
func TestOpenCodeGoObservedSearchResponseShape(t *testing.T) {
	body := `{"status":"completed","model":"gpt-5.6-luna","output":[{"type":"reasoning"},{"type":"web_search_call","status":"completed","action":{"type":"search","query":"example query"}},{"type":"web_search_call","status":"completed","action":{"type":"open_page","url":"https://example.com/source"}},{"type":"web_search_call","status":"completed","action":{"type":"find_in_page","url":"https://example.com/source","pattern":"example"}},{"type":"message","status":"completed","content":[{"type":"output_text","text":"Example [source](https://example.com/source).","annotations":[{"type":"url_citation","url":"https://example.com/source","title":"Source","start_index":0,"end_index":7}]}]}]}`
	answer, err := answerFixture(t, "opencode", body, http.StatusOK)
	if err != nil || !strings.Contains(answer.Text, "https://example.com/source") {
		t.Fatalf("answer = %#v, err = %v", answer, err)
	}
}

// These synthetic malformed-JSON and HTTP 429 cases exercise Searchwire's
// shared error handling; they are not copied provider-specific error bodies.
// HTTP 429 semantics: https://www.rfc-editor.org/rfc/rfc6585#section-4
// Provider response references are linked next to each fixture above.
func TestAnswerProviderErrorsOnInvalidResponses(t *testing.T) {
	for _, provider := range []string{"brave", "openrouter", "openai", "perplexity", "anthropic", "xai", "omniroute", "opencode"} {
		t.Run(provider, func(t *testing.T) {
			_, err := answerFixture(t, provider, `{`, http.StatusOK)
			if !errors.Is(err, ErrInvalidProviderResponse) {
				t.Fatalf("malformed JSON: got %v, want ErrInvalidProviderResponse", err)
			}
			_, err = answerFixture(t, provider, `{"error":{"message":"rate limited"}}`, http.StatusTooManyRequests)
			var httpErr *HTTPError
			if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusTooManyRequests {
				t.Fatalf("HTTP 429: got %v, want HTTPError status 429", err)
			}
		})
	}
}

// Search events and usage metadata differ across providers:
// https://developers.openai.com/api/docs/guides/tools-web-search
// https://openrouter.ai/docs/guides/features/server-tools/overview
func TestAnswerSearchStatus(t *testing.T) {
	for _, tc := range []struct {
		name, provider, body, want string
	}{
		{"openai search", "openai", `{"output":[{"type":"web_search_call","status":"completed"},{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`, "observed"},
		{"openai no event", "openai", `{"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`, "not_observed"},
		{"openrouter no usage", "openrouter", `{"choices":[{"message":{"content":"ok"}}]}`, "unknown"},
		{"openrouter search", "openrouter", `{"choices":[{"message":{"content":"ok"}}],"usage":{"server_tool_use":{"web_search_requests":1}}}`, "observed"},
		{"openrouter no search", "openrouter", `{"choices":[{"message":{"content":"ok"}}],"usage":{"server_tool_use":{"web_search_requests":0}}}`, "not_observed"},
		{"brave no event", "brave", `{"choices":[{"message":{"content":"ok"}}]}`, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			answer, err := answerFixture(t, tc.provider, tc.body, http.StatusOK)
			if err != nil || answer.SearchStatus != tc.want {
				t.Fatalf("answer=%#v err=%v, want status %q", answer, err, tc.want)
			}
		})
	}
}

// https://developers.openai.com/api/docs/guides/tools-web-search
func TestResponsesSearchFailureIsNotSuccess(t *testing.T) {
	body := `{"status":"completed","output":[{"type":"web_search_call","status":"failed"},{"type":"message","content":[{"type":"output_text","text":"Partial answer"}]}]}`
	answer, err := answerFixture(t, "openai", body, http.StatusOK)
	if err == nil {
		t.Fatalf("failed search returned as success: %#v", answer)
	}
}

// Citation annotation structure:
// https://openrouter.ai/docs/guides/features/plugins/web-search
func TestOpenRouterPreservesCitationAnnotations(t *testing.T) {
	body := `{"choices":[{"finish_reason":"stop","message":{"content":"Fact.","annotations":[{"type":"url_citation","url_citation":{"title":"Source","url":"https://example.com/source"}}]}}]}`
	answer, err := answerFixture(t, "openrouter", body, http.StatusOK)
	if err != nil || !strings.Contains(answer.Text, "https://example.com/source") {
		t.Fatalf("answer=%#v err=%v", answer, err)
	}
}

func TestAnswerTimeoutConfiguration(t *testing.T) {
	custom := &http.Client{Timeout: 3 * time.Second}
	for _, tc := range []struct {
		name                   string
		cfg                    Config
		wantSearch, wantAnswer time.Duration
	}{
		{"defaults", Config{}, 10 * time.Second, 60 * time.Second},
		{"explicit shared", Config{Timeout: 7 * time.Second}, 7 * time.Second, 7 * time.Second},
		{"separate answer", Config{Timeout: 7 * time.Second, AnswerTimeout: 20 * time.Second}, 7 * time.Second, 20 * time.Second},
		{"custom client", Config{HTTPClient: custom, AnswerTimeout: 20 * time.Second}, 3 * time.Second, 3 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := New(tc.cfg)
			if s.httpClient.(*http.Client).Timeout != tc.wantSearch || s.answerHTTPClient.(*http.Client).Timeout != tc.wantAnswer {
				t.Fatalf("search=%v answer=%v", s.httpClient.(*http.Client).Timeout, s.answerHTTPClient.(*http.Client).Timeout)
			}
		})
	}
}

// Citation and usage tags are delivered inside streamed content:
// https://api-dashboard.search.brave.com/documentation/services/answers
func TestBraveStreamSplitCitationAndIncomplete(t *testing.T) {
	stream := "data: {\"choices\":[{\"delta\":{\"content\":\"A fact <cita\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"tion>{\\\"url\\\":\\\"https://example.com/source\\\"}</citation>\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" + "data: [DONE]\n\n"
	answer, err := parseBraveAnswerStream([]byte(stream))
	if err != nil || !strings.Contains(answer, "A fact") || !strings.Contains(answer, "https://example.com/source") || strings.Contains(answer, "<citation>") {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
	if _, err := parseBraveAnswerStream([]byte(`data: {"choices":[{"delta":{"content":"partial"}}]}`)); !errors.Is(err, ErrInvalidProviderResponse) {
		t.Fatalf("interrupted stream err=%v", err)
	}
}

// https://api-dashboard.search.brave.com/documentation/services/answers
func TestBraveAnswerConsumesCitationStream(t *testing.T) {
	clearAnswerProviderKeys(t)
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		var request braveAnswerRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if !request.Stream || !request.EnableCitations {
			t.Fatalf("request = %#v", request)
		}
		body := "data: {\"choices\":[{\"delta\":{\"content\":\"Answer <citation>{\\\"url\\\":\\\"https://example.com/source\\\"}</citation>\"}}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	s := New(Config{HTTPClient: client, Brave: BraveConfig{APIKey: "fixture-key"}})
	answer, err := s.AnswerWithOptions(context.Background(), "question", AnswerOptions{Provider: "brave"})
	if err != nil || !strings.Contains(answer.Text, "https://example.com/source") {
		t.Fatalf("answer=%#v err=%v", answer, err)
	}
}

// Anthropic continuation must preserve tool state and stop after a bounded
// number of attempts if the provider repeatedly returns pause_turn:
// https://platform.claude.com/docs/en/agents-and-tools/tool-use/server-tools
func TestAnthropicPauseContinuationIsBounded(t *testing.T) {
	clearAnswerProviderKeys(t)
	calls := 0
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return answerJSONResponse(`{"stop_reason":"pause_turn","content":[{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"test"}}]}`), nil
	})}
	s := New(Config{HTTPClient: client, Anthropic: AnthropicConfig{APIKey: "fixture-key"}})
	_, err := s.AnswerWithOptions(context.Background(), "question", AnswerOptions{Provider: "anthropic"})
	if !errors.Is(err, ErrInvalidProviderResponse) || calls != 4 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func answerFixture(t *testing.T, provider, body string, status int) (*Answer, error) {
	t.Helper()
	clearAnswerProviderKeys(t)
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	cfg := Config{HTTPClient: client}
	switch provider {
	case "brave":
		cfg.Brave.APIKey = "fixture-key"
	case "openrouter":
		cfg.OpenRouter.APIKey = "fixture-key"
	case "openai":
		cfg.OpenAI.APIKey = "fixture-key"
	case "perplexity":
		cfg.Perplexity.APIKey = "fixture-key"
	case "anthropic":
		cfg.Anthropic.APIKey = "fixture-key"
	case "xai":
		cfg.XAI.APIKey = "fixture-key"
	case "omniroute":
		cfg.OmniRoute.APIKey = "fixture-key"
	case "opencode":
		cfg.OpenCode.APIKey = "fixture-key"
	default:
		t.Fatalf("unknown provider %q", provider)
	}
	return New(cfg).AnswerWithOptions(context.Background(), "test question", AnswerOptions{Provider: provider})
}

// These assertions protect source retention and error handling; they do not
// assert that every provider guarantees a search or nonempty sources.
func TestAnswerDesiredContract(t *testing.T) {
	// OpenAI documents url_citation annotations on output_text:
	// https://developers.openai.com/api/docs/guides/tools-web-search
	t.Run("openai structured citation reaches caller", func(t *testing.T) {
		body := `{"status":"completed","output":[{"type":"web_search_call","status":"completed"},{"type":"message","content":[{"type":"output_text","text":"A fact.","annotations":[{"type":"url_citation","start_index":0,"end_index":6,"title":"Source","url":"https://example.com/source"}]}]}]}`
		answer, err := answerFixture(t, "openai", body, http.StatusOK)
		if err != nil || !strings.Contains(answer.Text, "https://example.com/source") {
			t.Fatalf("citation did not reach caller: answer=%#v err=%v", answer, err)
		}
	})
	// Perplexity documents source URLs in the search_results output item:
	// https://github.com/perplexityai/api-platform-developers/blob/main/skills/migrate-sonar-to-agent-api/references/response-and-streaming.md
	t.Run("perplexity search results reach caller", func(t *testing.T) {
		body := `{"status":"completed","output":[{"type":"search_results","results":[{"id":"web:1","title":"Source","url":"https://example.com/source"}]},{"type":"message","content":[{"type":"output_text","text":"A fact [web:1]."}]}]}`
		answer, err := answerFixture(t, "perplexity", body, http.StatusOK)
		if err != nil || !strings.Contains(answer.Text, "https://example.com/source") || !strings.Contains(answer.Text, "- [web:1]") {
			t.Fatalf("search result did not reach caller: answer=%#v err=%v", answer, err)
		}
	})
	// Perplexity documents HTTP 200 responses with status failed or cancelled.
	// Combining failed status with partial text is a synthetic edge case:
	// https://github.com/perplexityai/api-platform-developers/blob/main/skills/migrate-sonar-to-agent-api/references/response-and-streaming.md
	t.Run("perplexity failed response with partial text is not success", func(t *testing.T) {
		body := `{"status":"failed","error":{"message":"agent failed"},"output":[{"type":"message","content":[{"type":"output_text","text":"Partial answer"}]}]}`
		answer, err := answerFixture(t, "perplexity", body, http.StatusOK)
		if err == nil {
			t.Fatalf("failed response accepted as successful: answer=%#v", answer)
		}
	})
	// OpenAI documents incomplete status and max_output_tokens as a reason.
	// The partial-text combination is synthetic:
	// https://developers.openai.com/api/reference/resources/responses/streaming-events
	t.Run("openai incomplete response with partial text is not success", func(t *testing.T) {
		body := `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","content":[{"type":"output_text","text":"Partial answer"}]}]}`
		answer, err := answerFixture(t, "openai", body, http.StatusOK)
		if err == nil {
			t.Fatalf("incomplete response accepted as successful: answer=%#v", answer)
		}
	})
	// OpenRouter identifies finish_reason=length as truncated output:
	// https://openrouter.ai/blog/extract-tables-from-pdf-llm/
	t.Run("openrouter truncated output is not success", func(t *testing.T) {
		body := `{"choices":[{"finish_reason":"length","message":{"role":"assistant","content":"Partial answer"}}]}`
		answer, err := answerFixture(t, "openrouter", body, http.StatusOK)
		if err == nil {
			t.Fatalf("truncated response accepted as successful: answer=%#v", answer)
		}
	})
	// The results wrapper is illustrated in this upstream issue; the combined
	// answer-plus-results fixture is synthetic, not an official API example:
	// https://github.com/diegosouzapw/OmniRoute/issues/10628
	t.Run("omniroute results retained with answer", func(t *testing.T) {
		body := `{"answer":"A fact.","results":[{"title":"Source","url":"https://example.com/source","snippet":"A fact."}]}`
		answer, err := answerFixture(t, "omniroute", body, http.StatusOK)
		if err != nil || !strings.Contains(answer.Text, "https://example.com/source") {
			t.Fatalf("results lost when answer is present: answer=%#v err=%v", answer, err)
		}
	})
	// Anthropic documents HTTP 200 web_search_tool_result_error blocks; the
	// combination with a text block is synthetic:
	// https://platform.claude.com/docs/en/agents-and-tools/tool-use/web-search-tool
	t.Run("anthropic search failure is surfaced", func(t *testing.T) {
		body := `{"stop_reason":"end_turn","content":[{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":{"type":"web_search_tool_result_error","error_code":"too_many_requests"}},{"type":"text","text":"I could not search."}]}`
		answer, err := answerFixture(t, "anthropic", body, http.StatusOK)
		if err == nil {
			t.Fatalf("search failure returned without structured error: answer=%#v", answer)
		}
	})
	// Anthropic documents sending the paused content and tools on continuation:
	// https://platform.claude.com/docs/en/agents-and-tools/tool-use/server-tools
	t.Run("anthropic pause turn continues", func(t *testing.T) {
		clearAnswerProviderKeys(t)
		calls := 0
		client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			if calls == 2 {
				var continuation struct {
					Messages []struct {
						Role    string          `json:"role"`
						Content json.RawMessage `json:"content"`
					} `json:"messages"`
					Tools json.RawMessage `json:"tools"`
				}
				if err := json.NewDecoder(r.Body).Decode(&continuation); err != nil {
					t.Fatalf("decode continuation: %v", err)
				}
				if len(continuation.Messages) != 2 || continuation.Messages[1].Role != "assistant" || !strings.Contains(string(continuation.Messages[1].Content), `"srvtoolu_1"`) || !strings.Contains(string(continuation.Tools), `"web_search_20250305"`) {
					t.Fatalf("continuation lost paused assistant content or tool: %#v", continuation)
				}
			}
			body := `{"stop_reason":"pause_turn","content":[{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"test"}}]}`
			if calls > 1 {
				body = `{"stop_reason":"end_turn","content":[{"type":"text","text":"Completed answer."}]}`
			}
			return answerJSONResponse(body), nil
		})}
		answer, err := New(Config{HTTPClient: client, Anthropic: AnthropicConfig{APIKey: "fixture-key"}}).AnswerWithOptions(context.Background(), "test question", AnswerOptions{Provider: "anthropic"})
		if err != nil || calls != 2 || answer.Text != "Completed answer." {
			t.Fatalf("pause was not continued: calls=%d answer=%#v err=%v", calls, answer, err)
		}
	})
}
