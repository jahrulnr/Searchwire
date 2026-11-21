package searchwire

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Brave's citation tags are carried inside streaming delta content, and may
// be split across multiple events. Assemble text before extracting the tags.
// https://api-dashboard.search.brave.com/documentation/services/answers
func parseBraveAnswerStream(body []byte) (string, error) {
	var text strings.Builder
	terminal := false
	for event := range strings.SplitSeq(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n\n") {
		var data []string
		for line := range strings.SplitSeq(event, "\n") {
			if after, ok := strings.CutPrefix(line, "data:"); ok {
				data = append(data, strings.TrimPrefix(after, " "))
			}
		}
		if len(data) == 0 {
			continue
		}
		payload := strings.Join(data, "\n")
		if payload == "[DONE]" {
			terminal = true
			continue
		}
		var chunk struct {
			Error   json.RawMessage `json:"error"`
			Choices []struct {
				FinishReason string `json:"finish_reason"`
				Delta        struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return "", fmt.Errorf("%w: parse brave stream: %v", ErrInvalidProviderResponse, err)
		}
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			return "", fmt.Errorf("%w: brave stream error", ErrInvalidProviderResponse)
		}
		for _, choice := range chunk.Choices {
			text.WriteString(choice.Delta.Content)
			if choice.FinishReason != "" {
				if choice.FinishReason != "stop" {
					return "", fmt.Errorf("%w: brave finish reason %s", ErrInvalidProviderResponse, choice.FinishReason)
				}
				terminal = true
			}
		}
	}
	if !terminal {
		return "", fmt.Errorf("%w: brave stream ended without completion", ErrInvalidProviderResponse)
	}
	value := text.String()
	var clean strings.Builder
	var sources []answerSource
	for len(value) > 0 {
		i := strings.IndexByte(value, '<')
		if i < 0 {
			clean.WriteString(value)
			break
		}
		clean.WriteString(value[:i])
		value = value[i:]
		tag := ""
		if strings.HasPrefix(value, "<citation>") {
			tag = "citation"
		}
		if strings.HasPrefix(value, "<usage>") {
			tag = "usage"
		}
		if tag == "" {
			clean.WriteByte(value[0])
			value = value[1:]
			continue
		}
		endTag := "</" + tag + ">"
		end := strings.Index(value, endTag)
		if end < 0 {
			return "", fmt.Errorf("%w: incomplete brave %s tag", ErrInvalidProviderResponse, tag)
		}
		if tag == "citation" {
			var source answerSource
			if err := json.Unmarshal([]byte(value[len("<citation>"):end]), &source); err != nil {
				return "", fmt.Errorf("%w: parse brave citation: %v", ErrInvalidProviderResponse, err)
			}
			sources = append(sources, source)
		}
		value = value[end+len(endTag):]
	}
	answer := strings.TrimSpace(clean.String())
	if answer == "" {
		return "", ErrInvalidProviderResponse
	}
	return appendAnswerSources(answer, "Sources", sources), nil
}
