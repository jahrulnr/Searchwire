package searchwire

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/url"
	"strconv"
	"strings"
)

// stackExchangeMaxPageSize is the API's hard page-size ceiling.
const stackExchangeMaxPageSize = 100

// stackExchangeSource queries the Stack Exchange API excerpt search, which
// returns highlighted excerpts for both questions and answers in relevance
// order. Without a key the API allows ~300 requests/day per IP; a free key
// (STACKEXCHANGE_API_KEY) raises that to 10,000.
type stackExchangeSource struct {
	apiKey string
	site   string
}

func (stackExchangeSource) name() string { return "stackexchange" }

func (s stackExchangeSource) search(ctx context.Context, f *fetcher, query string, limit int) ([]sourceResult, error) {
	if limit <= 0 {
		limit = defaultLimit
	}
	site := s.site
	if site == "" {
		site = stackExchangeDefaultSite
	}
	endpoint := url.URL{Scheme: "https", Host: "api.stackexchange.com", Path: "/2.3/search/excerpts"}
	params := endpoint.Query()
	params.Set("q", query)
	params.Set("site", site)
	params.Set("order", "desc")
	params.Set("sort", "relevance")
	// Over-fetch: negative-score items and answers that collapse into an
	// already-listed question are dropped by the parser.
	params.Set("pagesize", strconv.Itoa(min(limit*2, stackExchangeMaxPageSize)))
	if s.apiKey != "" {
		params.Set("key", s.apiKey)
	}
	endpoint.RawQuery = params.Encode()

	body, err := f.get(ctx, endpoint.String(), "application/json", []string{"application/json"})
	if err != nil {
		return nil, redactErrorSecret(err, s.apiKey)
	}
	return parseStackExchangeResults(body, site, limit)
}

type stackExchangeResponse struct {
	Items []struct {
		ItemType          string   `json:"item_type"`
		QuestionID        int64    `json:"question_id"`
		AnswerID          int64    `json:"answer_id"`
		Title             string   `json:"title"`
		Excerpt           string   `json:"excerpt"`
		Score             int      `json:"score"`
		QuestionScore     int      `json:"question_score"`
		AnswerCount       int      `json:"answer_count"`
		IsAnswered        bool     `json:"is_answered"`
		HasAcceptedAnswer bool     `json:"has_accepted_answer"`
		IsAccepted        bool     `json:"is_accepted"`
		Tags              []string `json:"tags"`
	} `json:"items"`
	ErrorID      int    `json:"error_id"`
	ErrorMessage string `json:"error_message"`
}

func parseStackExchangeResults(body []byte, site string, limit int) ([]sourceResult, error) {
	body, err := maybeGunzip(body)
	if err != nil {
		return nil, fmt.Errorf("decode stackexchange response: %w", err)
	}
	var payload stackExchangeResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode stackexchange response: %w", err)
	}
	if payload.ErrorID != 0 {
		return nil, fmt.Errorf("stackexchange error %d: %s", payload.ErrorID, payload.ErrorMessage)
	}
	host := stackExchangeHost(site)
	seen := make(map[int64]bool, len(payload.Items))
	results := make([]sourceResult, 0, min(limit, len(payload.Items)))
	for _, item := range payload.Items {
		if len(results) >= limit {
			break
		}
		// Downvoted questions are almost always unclear or wrong; the
		// relevance sort does not account for that.
		if item.QuestionID == 0 || item.QuestionScore < 0 || item.Score < 0 || seen[item.QuestionID] {
			continue
		}
		title := collapseWhitespace(html.UnescapeString(item.Title))
		if title == "" {
			continue
		}
		seen[item.QuestionID] = true
		link := fmt.Sprintf("https://%s/questions/%d", host, item.QuestionID)
		if item.ItemType == "answer" && item.AnswerID != 0 {
			link = fmt.Sprintf("https://%s/a/%d", host, item.AnswerID)
		}

		meta := []string{}
		if item.ItemType == "answer" {
			meta = append(meta, fmt.Sprintf("answer, score %d", item.Score))
			if item.IsAccepted {
				meta = append(meta, "accepted")
			}
		} else {
			meta = append(meta, fmt.Sprintf("question, score %d", item.QuestionScore))
			answers := fmt.Sprintf("%d answers", item.AnswerCount)
			if item.AnswerCount == 1 {
				answers = "1 answer"
			}
			meta = append(meta, answers)
			if item.HasAcceptedAnswer {
				meta = append(meta, "has accepted answer")
			}
		}
		if len(item.Tags) > 0 {
			meta = append(meta, "tags: "+strings.Join(item.Tags, ", "))
		}
		snippet := "[" + strings.Join(meta, " · ") + "] " + strings.ReplaceAll(htmlText(item.Excerpt), "…", "… ")
		results = append(results, sourceResult{
			title:   title,
			url:     link,
			snippet: collapseWhitespace(snippet),
			rank:    len(results) + 1,
		})
	}
	return results, nil
}

// stackExchangeHost maps an API site parameter to its web host:
// "stackoverflow" → stackoverflow.com, "unix" → unix.stackexchange.com.
// A value that already looks like a host is used as-is.
func stackExchangeHost(site string) string {
	switch {
	case strings.Contains(site, "."):
		return site
	case site == "stackoverflow", site == "serverfault", site == "superuser", site == "askubuntu":
		return site + ".com"
	default:
		return site + ".stackexchange.com"
	}
}

// maybeGunzip decompresses a gzip body. The Stack Exchange API always
// compresses responses; the stdlib transport undoes that transparently, but
// a caller-supplied client with compression disabled would hand us raw gzip.
func maybeGunzip(body []byte) ([]byte, error) {
	if len(body) < 2 || body[0] != 0x1f || body[1] != 0x8b {
		return body, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return io.ReadAll(zr)
}
