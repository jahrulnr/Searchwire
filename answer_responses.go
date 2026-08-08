package searchwire

import (
	"encoding/json"
	"fmt"
	"strings"
)

type responsesAnswerResponse struct {
	Output []struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
}

func parseResponsesAnswer(body []byte, provider string) (string, error) {
	var response responsesAnswerResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("%w: parse %s answer: %v", ErrInvalidProviderResponse, provider, err)
	}
	parts := make([]string, 0, 1)
	for _, output := range response.Output {
		if output.Type != "message" {
			continue
		}
		for _, content := range output.Content {
			if content.Type == "output_text" && strings.TrimSpace(content.Text) != "" {
				parts = append(parts, strings.TrimSpace(content.Text))
			}
		}
	}
	answer := strings.TrimSpace(strings.Join(parts, "\n\n"))
	if answer == "" {
		return "", ErrInvalidProviderResponse
	}
	return answer, nil
}
