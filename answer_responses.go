package searchwire

import (
	"encoding/json"
	"fmt"
	"strings"
)

type answerSource struct {
	ID    string `json:"id"`
	URL   string `json:"url"`
	Title string `json:"title"`
}

type responsesAnswerResponse struct {
	Status            string          `json:"status"`
	IncompleteDetails json.RawMessage `json:"incomplete_details"`
	Output            []struct {
		Type    string         `json:"type"`
		Status  string         `json:"status"`
		Results []answerSource `json:"results"`
		Content []struct {
			Type        string         `json:"type"`
			Text        string         `json:"text"`
			Annotations []answerSource `json:"annotations"`
		} `json:"content"`
	} `json:"output"`
	Citations []string `json:"citations"`
}

func parseResponsesAnswer(body []byte, provider string) (string, error) {
	var response responsesAnswerResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("%w: parse %s answer: %v", ErrInvalidProviderResponse, provider, err)
	}
	if response.Status != "" && response.Status != "completed" {
		return "", fmt.Errorf("%w: %s response status %s", ErrInvalidProviderResponse, provider, response.Status)
	}
	parts := make([]string, 0, 1)
	var cited, results []answerSource
	for _, output := range response.Output {
		if output.Type == "web_search_call" && output.Status != "" && output.Status != "completed" {
			return "", fmt.Errorf("%w: %s web search status %s", ErrInvalidProviderResponse, provider, output.Status)
		}
		if output.Type == "search_results" {
			results = append(results, output.Results...)
		}
		if output.Type != "message" {
			continue
		}
		for _, content := range output.Content {
			if content.Type == "output_text" && strings.TrimSpace(content.Text) != "" {
				parts = append(parts, strings.TrimSpace(content.Text))
				cited = append(cited, content.Annotations...)
			}
		}
	}
	answer := strings.TrimSpace(strings.Join(parts, "\n\n"))
	if answer == "" {
		return "", ErrInvalidProviderResponse
	}
	for _, url := range response.Citations {
		results = append(results, answerSource{URL: url})
	}
	answer = appendAnswerSources(answer, "Sources", cited)
	answer = appendAnswerSources(answer, "Search results", results)
	return answer, nil
}

// A missing event is reported as not observed, not proof that search did not
// happen: gateways differ in which events they expose.
func responsesSearchStatus(body []byte) string {
	var response responsesAnswerResponse
	if json.Unmarshal(body, &response) != nil || response.Output == nil {
		return "unknown"
	}
	for _, item := range response.Output {
		if item.Type == "web_search_call" || item.Type == "search_results" {
			return "observed"
		}
	}
	return "not_observed"
}

// appendAnswerSources retains links the provider returns separately from text.
// Search results are labeled separately from citations because a retrieved page
// is not necessarily a source used in the final answer.
func appendAnswerSources(answer, heading string, sources []answerSource) string {
	seen := make(map[string]bool, len(sources))
	var lines []string
	for _, source := range sources {
		url := strings.TrimSpace(source.URL)
		if !isSafeResultURL(url) || seen[url] || strings.Contains(answer, url) {
			continue
		}
		seen[url] = true
		title := strings.TrimSpace(source.Title)
		if title == "" {
			title = url
		}
		label := ""
		if id := strings.TrimSpace(source.ID); id != "" {
			label = "[" + escapeMarkdownLinkText(id) + "] "
		}
		lines = append(lines, "- "+label+"["+escapeMarkdownLinkText(title)+"]("+url+")")
	}
	if len(lines) == 0 {
		return answer
	}
	return answer + "\n\n" + heading + ":\n" + strings.Join(lines, "\n")
}
