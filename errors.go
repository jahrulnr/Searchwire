package searchwire

import (
	"errors"
	"fmt"
	"strings"
)

var (
	ErrEmptyQuery            = errors.New("searchwire: query is required")
	ErrEmptyQuestion         = errors.New("searchwire: question is required")
	ErrEmptyURL              = errors.New("searchwire: url is required")
	ErrInvalidURL            = errors.New("searchwire: invalid url")
	ErrUnsupportedScheme     = errors.New("searchwire: unsupported url scheme")
	ErrNilSearcher           = errors.New("searchwire: nil searcher")
	ErrResponseTooLarge      = errors.New("searchwire: response exceeds configured limit")
	ErrUnexpectedContentType = errors.New("searchwire: unexpected content type")
	// ErrLLMsTxtNotFound is returned by FetchLLMsTxt when no llms.txt file
	// exists at the enclosing directory or the site root (all probes 404/410).
	ErrLLMsTxtNotFound = errors.New("searchwire: llms.txt not found")
	// ErrInvalidLLMsTxt is returned when llms.txt content exists but violates
	// the required structure (missing H1 heading).
	ErrInvalidLLMsTxt            = errors.New("searchwire: invalid llms.txt")
	ErrAnswerUnavailable         = errors.New("searchwire: answer provider is unavailable")
	ErrAnswerProviderUnavailable = errors.New("searchwire: requested answer provider is unavailable")
	ErrInvalidProviderResponse   = errors.New("searchwire: invalid provider response")
)

// SearchError is returned when every built-in source fails.
type SearchError struct {
	Failures []SourceError
}

func (e *SearchError) Error() string {
	if e == nil || len(e.Failures) == 0 {
		return "searchwire: all sources failed"
	}
	parts := make([]string, len(e.Failures))
	for i, f := range e.Failures {
		parts[i] = f.Source + ": " + f.Error
	}
	return "searchwire: all sources failed: " + strings.Join(parts, "; ")
}

// SourceError records one source failure without aborting the whole search.
type SourceError struct {
	Source string
	Error  string
}

// HTTPError reports a non-2xx HTTP response from a source.
type HTTPError struct {
	StatusCode int
	Status     string
	Body       string
	// RetryAfter is the parsed Retry-After header value in seconds when the
	// server provided one (e.g. on 429/503). Zero when absent or unparseable.
	RetryAfter int
	// ErrorBody is the structured error envelope parsed from Body when it is
	// valid JSON of the form {"error":...} or {"errors":[...]} or
	// {"message":"..."} or {"detail":"..."}. Nil when Body is not JSON or
	// does not match a known shape.
	ErrorBody map[string]any
}

func (e *HTTPError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("searchwire: source returned %s", e.Status)
	}
	return fmt.Sprintf("searchwire: source returned %s: %s", e.Status, cleanErrorBody(e.Body))
}

func redactErrorSecret(err error, secret string) error {
	if err == nil || secret == "" || !strings.Contains(err.Error(), secret) {
		return err
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return &HTTPError{
			StatusCode: httpErr.StatusCode,
			Status:     strings.ReplaceAll(httpErr.Status, secret, "[redacted]"),
			Body:       strings.ReplaceAll(httpErr.Body, secret, "[redacted]"),
			RetryAfter: httpErr.RetryAfter,
			ErrorBody:  httpErr.ErrorBody,
		}
	}
	return errors.New(strings.ReplaceAll(err.Error(), secret, "[redacted]"))
}
