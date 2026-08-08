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

func TestAnswerUnavailableWithoutAPIKey(t *testing.T) {
	t.Setenv("BRAVE_SEARCH_API_KEY", "")
	searcher := New(Config{})
	if searcher.CanAnswer() {
		t.Fatal("CanAnswer returned true without an API key")
	}
	if _, err := searcher.Answer(context.Background(), "What is Go?"); !errors.Is(err, ErrAnswerUnavailable) {
		t.Fatalf("err = %v", err)
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
