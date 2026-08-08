package searchwire

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

type braveSource struct {
	apiKey string
}

func (braveSource) name() string { return "brave" }

func (b braveSource) search(ctx context.Context, f *fetcher, query string, limit int) ([]sourceResult, error) {
	if limit <= 0 {
		limit = defaultLimit
	}
	if b.apiKey != "" {
		return b.searchAPI(ctx, f, query, limit)
	}
	endpoint := url.URL{
		Scheme: "https",
		Host:   "search.brave.com",
		Path:   "/search",
	}
	params := endpoint.Query()
	params.Set("q", query)
	params.Set("source", "web")
	endpoint.RawQuery = params.Encode()

	body, err := f.get(ctx, endpoint.String(), "text/html", []string{"text/html"})
	if err != nil {
		return nil, err
	}
	return parseBraveResults(body, limit)
}

func (b braveSource) searchAPI(ctx context.Context, f *fetcher, query string, limit int) ([]sourceResult, error) {
	endpoint := url.URL{
		Scheme: "https",
		Host:   "api.search.brave.com",
		Path:   "/res/v1/web/search",
	}
	params := endpoint.Query()
	params.Set("q", query)
	params.Set("count", strconv.Itoa(limit))
	endpoint.RawQuery = params.Encode()

	headers := http.Header{"X-Subscription-Token": []string{b.apiKey}}
	body, err := f.getWithHeaders(ctx, endpoint.String(), "application/json", []string{"application/json"}, headers)
	if err != nil {
		return nil, err
	}
	return parseBraveAPIResults(body, limit)
}

type braveAPIResponse struct {
	Web struct {
		Results []struct {
			Title       string `json:"title"`
			URL         string `json:"url"`
			Description string `json:"description"`
		} `json:"results"`
	} `json:"web"`
}

func parseBraveAPIResults(body []byte, limit int) ([]sourceResult, error) {
	var payload braveAPIResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parse brave api response: %w", err)
	}
	results := make([]sourceResult, 0, min(limit, len(payload.Web.Results)))
	for _, item := range payload.Web.Results {
		if len(results) >= limit {
			break
		}
		title := collapseWhitespace(item.Title)
		resultURL := strings.TrimSpace(item.URL)
		if title == "" || !isSafeResultURL(resultURL) {
			continue
		}
		results = append(results, sourceResult{
			title:   title,
			url:     resultURL,
			snippet: htmlText(item.Description),
			rank:    len(results) + 1,
		})
	}
	return results, nil
}

func parseBraveResults(body []byte, limit int) ([]sourceResult, error) {
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("parse brave html: %w", err)
	}
	results := make([]sourceResult, 0, limit)
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if len(results) >= limit {
			return
		}
		if n.Type == html.ElementNode && n.Data == "div" {
			if attrValue(n, "data-type") == "web" && hasClassToken(attrValue(n, "class"), "snippet") {
				if item, ok := parseBraveBlock(n); ok {
					item.rank = len(results) + 1
					results = append(results, item)
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	return results, nil
}

func parseBraveBlock(root *html.Node) (sourceResult, bool) {
	linkNode := findDescendantAnchor(root)
	if linkNode == nil {
		return sourceResult{}, false
	}
	resultURL := strings.TrimSpace(attrValue(linkNode, "href"))
	if !isSafeResultURL(resultURL) {
		return sourceResult{}, false
	}

	titleNode := findDescendant(root, "div", "search-snippet-title")
	if titleNode == nil {
		titleNode = findDescendant(root, "div", "title")
	}
	title := textContent(titleNode)
	if title == "" {
		title = textContent(linkNode)
	}
	if title == "" {
		return sourceResult{}, false
	}

	snippetNode := findDescendant(root, "div", "generic-snippet")
	snippet := ""
	if snippetNode != nil {
		if content := snippetNode.FirstChild; content != nil {
			snippet = textContent(content)
		}
		if snippet == "" {
			snippet = textContent(snippetNode)
		}
	}

	return sourceResult{title: title, url: resultURL, snippet: snippet}, true
}
