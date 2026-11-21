package searchwire

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Scholarly sources are opt-in: academic indexes add latency and noise to
// everyday web queries, so they join a search only when named in
// SearchOptions.Sources or Config.DefaultSources. All of them point a hit at
// its DOI URL when one exists, so the same paper found by several indexes
// fuses into one corroborated result.

// scholarlyAbstractMaxLen bounds the abstract kept in a snippet before the
// tool layer applies its own cap.
const scholarlyAbstractMaxLen = 600

type scholarlyMeta struct {
	year      int
	venue     string
	citations int // <0 = unknown
	kind      string
	pdf       string
}

// scholarlySnippet renders "[2025 · Venue · 934 citations · open-access PDF: …] abstract".
func scholarlySnippet(m scholarlyMeta, abstract string) string {
	parts := make([]string, 0, 5)
	if m.year > 0 {
		parts = append(parts, strconv.Itoa(m.year))
	}
	if v := collapseWhitespace(m.venue); v != "" {
		parts = append(parts, v)
	}
	if m.kind != "" {
		parts = append(parts, m.kind)
	}
	if m.citations >= 0 {
		parts = append(parts, fmt.Sprintf("%d citations", m.citations))
	}
	if m.pdf != "" {
		parts = append(parts, "open-access PDF: "+m.pdf)
	}
	snippet := ""
	if len(parts) > 0 {
		snippet = "[" + strings.Join(parts, " · ") + "]"
	}
	if abstract = sanitizeSnippet(abstract); abstract != "" {
		snippet = strings.TrimSpace(snippet + " " + truncateSnippet(abstract, scholarlyAbstractMaxLen))
	}
	return snippet
}

// doiURL normalizes a bare DOI or doi.org URL to https://doi.org/<lowercase
// doi>. DOIs are case-insensitive, so lowercasing makes the fusion key match
// across indexes. Returns "" for an empty or malformed value.
func doiURL(doi string) string {
	doi = strings.TrimSpace(doi)
	for _, prefix := range []string{"https://doi.org/", "http://doi.org/", "https://dx.doi.org/", "http://dx.doi.org/", "doi:"} {
		if len(doi) >= len(prefix) && strings.EqualFold(doi[:len(prefix)], prefix) {
			doi = doi[len(prefix):]
			break
		}
	}
	if !strings.HasPrefix(doi, "10.") || !strings.Contains(doi, "/") {
		return ""
	}
	return "https://doi.org/" + strings.ToLower(doi)
}

// scholarlyTermPattern splits a free-text query into terms for APIs whose
// query syntax ORs bare words (arXiv).
var scholarlyTermPattern = regexp.MustCompile(`[\pL\pN][\pL\pN.+#-]*`)

// ---------------------------------------------------------------- OpenAlex

type openAlexSource struct{ apiKey string }

func (openAlexSource) name() string { return "openalex" }
func (openAlexSource) optIn() bool  { return true }

func (s openAlexSource) search(ctx context.Context, f *fetcher, query string, limit int) ([]sourceResult, error) {
	if limit <= 0 {
		limit = defaultLimit
	}
	endpoint := url.URL{Scheme: "https", Host: "api.openalex.org", Path: "/works"}
	params := endpoint.Query()
	params.Set("search", query)
	params.Set("per_page", strconv.Itoa(min(limit, 50)))
	// select trims each work from ~4 KB to the fields we render.
	params.Set("select", "id,doi,display_name,publication_year,cited_by_count,type,primary_location,open_access,abstract_inverted_index")
	if s.apiKey != "" {
		params.Set("api_key", s.apiKey)
	}
	endpoint.RawQuery = params.Encode()
	body, err := f.get(ctx, endpoint.String(), "application/json", []string{"application/json"})
	if err != nil {
		return nil, redactErrorSecret(err, s.apiKey)
	}
	return parseOpenAlexResults(body, limit)
}

type openAlexResponse struct {
	Results []struct {
		ID              string `json:"id"`
		DOI             string `json:"doi"`
		DisplayName     string `json:"display_name"`
		PublicationYear int    `json:"publication_year"`
		CitedByCount    int    `json:"cited_by_count"`
		Type            string `json:"type"`
		PrimaryLocation *struct {
			LandingPageURL string `json:"landing_page_url"`
			Source         *struct {
				DisplayName string `json:"display_name"`
			} `json:"source"`
		} `json:"primary_location"`
		OpenAccess struct {
			OAURL string `json:"oa_url"`
		} `json:"open_access"`
		AbstractInvertedIndex map[string][]int `json:"abstract_inverted_index"`
	} `json:"results"`
}

func parseOpenAlexResults(body []byte, limit int) ([]sourceResult, error) {
	var payload openAlexResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode openalex response: %w", err)
	}
	results := make([]sourceResult, 0, min(limit, len(payload.Results)))
	for _, w := range payload.Results {
		if len(results) >= limit {
			break
		}
		title := collapseWhitespace(w.DisplayName)
		link := doiURL(w.DOI)
		meta := scholarlyMeta{year: w.PublicationYear, citations: w.CitedByCount, kind: w.Type}
		if pl := w.PrimaryLocation; pl != nil {
			if link == "" && isSafeResultURL(pl.LandingPageURL) {
				link = pl.LandingPageURL
			}
			if pl.Source != nil {
				meta.venue = pl.Source.DisplayName
			}
		}
		if link == "" && isSafeResultURL(w.ID) {
			link = w.ID
		}
		if title == "" || link == "" {
			continue
		}
		if isSafeResultURL(w.OpenAccess.OAURL) && w.OpenAccess.OAURL != link {
			meta.pdf = w.OpenAccess.OAURL
		}
		results = append(results, sourceResult{
			title:   title,
			url:     link,
			snippet: scholarlySnippet(meta, openAlexAbstract(w.AbstractInvertedIndex)),
			rank:    len(results) + 1,
		})
	}
	return results, nil
}

// openAlexAbstract rebuilds plain text from OpenAlex's inverted index
// (word → positions), which it ships instead of the abstract for licensing
// reasons.
func openAlexAbstract(index map[string][]int) string {
	if len(index) == 0 {
		return ""
	}
	type pos struct {
		at   int
		word string
	}
	words := make([]pos, 0, len(index)*2)
	for word, positions := range index {
		for _, at := range positions {
			words = append(words, pos{at, word})
		}
	}
	sort.Slice(words, func(i, j int) bool { return words[i].at < words[j].at })
	var b strings.Builder
	for i, w := range words {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(w.word)
		if b.Len() > scholarlyAbstractMaxLen*2 {
			break
		}
	}
	return b.String()
}

// ------------------------------------------------------------------- arXiv

// arXiv asks API clients to wait 3 seconds between requests. The limiter is
// process-wide so concurrent searches queue instead of tripping it.
const arXivMinInterval = 3 * time.Second

var arXivLimiter = &intervalLimiter{interval: arXivMinInterval}

type intervalLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

// wait blocks until the next request slot or ctx ends.
func (l *intervalLimiter) wait(ctx context.Context) error {
	l.mu.Lock()
	now := time.Now()
	slot := l.next
	if slot.Before(now) {
		slot = now
	}
	l.next = slot.Add(l.interval)
	l.mu.Unlock()
	if d := time.Until(slot); d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
	return nil
}

type arXivSource struct{ limiter *intervalLimiter }

func (arXivSource) name() string { return "arxiv" }
func (arXivSource) optIn() bool  { return true }

func (s arXivSource) search(ctx context.Context, f *fetcher, query string, limit int) ([]sourceResult, error) {
	if limit <= 0 {
		limit = defaultLimit
	}
	terms := scholarlyTermPattern.FindAllString(query, 12)
	if len(terms) == 0 {
		return []sourceResult{}, nil
	}
	// Bare words in search_query are ORed; AND them so a multi-word query
	// means what it says.
	for i, t := range terms {
		terms[i] = "all:" + t
	}
	endpoint := url.URL{Scheme: "https", Host: "export.arxiv.org", Path: "/api/query"}
	params := endpoint.Query()
	params.Set("search_query", strings.Join(terms, " AND "))
	params.Set("sortBy", "relevance")
	params.Set("max_results", strconv.Itoa(min(limit, 50)))
	endpoint.RawQuery = params.Encode()

	limiter := s.limiter
	if limiter == nil {
		limiter = arXivLimiter
	}
	if err := limiter.wait(ctx); err != nil {
		return nil, err
	}
	body, err := f.get(ctx, endpoint.String(), "application/atom+xml", []string{"application/atom+xml", "application/xml", "text/xml"})
	if err != nil {
		return nil, err
	}
	return parseArXivResults(body, limit)
}

type arXivFeed struct {
	Entries []struct {
		ID        string `xml:"id"`
		Title     string `xml:"title"`
		Summary   string `xml:"summary"`
		Published string `xml:"published"`
		DOI       string `xml:"http://arxiv.org/schemas/atom doi"`
		Journal   string `xml:"http://arxiv.org/schemas/atom journal_ref"`
		Primary   struct {
			Term string `xml:"term,attr"`
		} `xml:"http://arxiv.org/schemas/atom primary_category"`
		Links []struct {
			Href  string `xml:"href,attr"`
			Title string `xml:"title,attr"`
			Type  string `xml:"type,attr"`
		} `xml:"link"`
	} `xml:"entry"`
}

var arXivVersionSuffix = regexp.MustCompile(`v\d+$`)

func parseArXivResults(body []byte, limit int) ([]sourceResult, error) {
	var feed arXivFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("decode arxiv feed: %w", err)
	}
	results := make([]sourceResult, 0, min(limit, len(feed.Entries)))
	for _, e := range feed.Entries {
		if len(results) >= limit {
			break
		}
		title := collapseWhitespace(e.Title)
		// Version-less abs URL: stable across revisions and dedups.
		abs := arXivVersionSuffix.ReplaceAllString(strings.Replace(strings.TrimSpace(e.ID), "http://", "https://", 1), "")
		// A malformed query comes back as a single entry whose id points
		// at /api/errors; it is not a paper.
		if title == "" || !isSafeResultURL(abs) || strings.Contains(abs, "/api/errors") {
			continue
		}
		link := abs
		if d := doiURL(e.DOI); d != "" {
			link = d
		}
		meta := scholarlyMeta{citations: -1, venue: e.Journal, kind: "preprint " + e.Primary.Term}
		if len(e.Published) >= 4 {
			meta.year, _ = strconv.Atoi(e.Published[:4])
		}
		if link != abs {
			meta.kind += " · arXiv: " + abs
		}
		for _, l := range e.Links {
			if l.Title == "pdf" || l.Type == "application/pdf" {
				meta.pdf = arXivVersionSuffix.ReplaceAllString(l.Href, "")
			}
		}
		results = append(results, sourceResult{
			title:   title,
			url:     link,
			snippet: scholarlySnippet(meta, e.Summary),
			rank:    len(results) + 1,
		})
	}
	return results, nil
}

// --------------------------------------------------------------- Europe PMC

type europePMCSource struct{}

func (europePMCSource) name() string { return "europepmc" }
func (europePMCSource) optIn() bool  { return true }

func (europePMCSource) search(ctx context.Context, f *fetcher, query string, limit int) ([]sourceResult, error) {
	if limit <= 0 {
		limit = defaultLimit
	}
	endpoint := url.URL{Scheme: "https", Host: "www.ebi.ac.uk", Path: "/europepmc/webservices/rest/search"}
	params := endpoint.Query()
	params.Set("query", query)
	params.Set("format", "json")
	params.Set("resultType", "core")
	params.Set("pageSize", strconv.Itoa(min(limit, 100)))
	endpoint.RawQuery = params.Encode()
	body, err := f.get(ctx, endpoint.String(), "application/json", []string{"application/json"})
	if err != nil {
		return nil, err
	}
	return parseEuropePMCResults(body, limit)
}

type europePMCResponse struct {
	ResultList struct {
		Result []struct {
			ID           string `json:"id"`
			Source       string `json:"source"`
			DOI          string `json:"doi"`
			Title        string `json:"title"`
			PubYear      string `json:"pubYear"`
			CitedByCount int    `json:"citedByCount"`
			AbstractText string `json:"abstractText"`
			JournalInfo  *struct {
				Journal struct {
					Title string `json:"title"`
				} `json:"journal"`
			} `json:"journalInfo"`
			BookOrReportDetails *struct {
				Publisher string `json:"publisher"`
			} `json:"bookOrReportDetails"`
			FullTextURLList *struct {
				FullTextURL []struct {
					AvailabilityCode string `json:"availabilityCode"`
					DocumentStyle    string `json:"documentStyle"`
					URL              string `json:"url"`
				} `json:"fullTextUrl"`
			} `json:"fullTextUrlList"`
		} `json:"result"`
	} `json:"resultList"`
}

func parseEuropePMCResults(body []byte, limit int) ([]sourceResult, error) {
	var payload europePMCResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode europepmc response: %w", err)
	}
	items := payload.ResultList.Result
	results := make([]sourceResult, 0, min(limit, len(items)))
	seen := make(map[string]bool, len(items))
	for _, r := range items {
		if len(results) >= limit {
			break
		}
		title := strings.TrimRight(collapseWhitespace(r.Title), ".")
		if title == "" || r.ID == "" || r.Source == "" {
			continue
		}
		link := doiURL(r.DOI)
		if link == "" {
			link = "https://europepmc.org/article/" + url.PathEscape(r.Source) + "/" + url.PathEscape(r.ID)
		}
		// Europe PMC lists the MEDLINE record and the PMC full text of one
		// paper separately, and only one of them may carry the DOI; key on
		// the normalized title as well.
		titleKey := "t:" + strings.ToLower(title)
		if seen[link] || seen[titleKey] {
			continue
		}
		seen[link], seen[titleKey] = true, true
		meta := scholarlyMeta{citations: r.CitedByCount}
		meta.year, _ = strconv.Atoi(r.PubYear)
		if r.JournalInfo != nil {
			meta.venue = r.JournalInfo.Journal.Title
		}
		if r.Source == "PPR" {
			meta.kind = "preprint"
		}
		if r.FullTextURLList != nil {
			for _, ft := range r.FullTextURLList.FullTextURL {
				if (ft.AvailabilityCode == "F" || ft.AvailabilityCode == "OA") && ft.DocumentStyle == "pdf" && isSafeResultURL(ft.URL) {
					meta.pdf = ft.URL
					break
				}
			}
		}
		results = append(results, sourceResult{
			title:   title,
			url:     link,
			snippet: scholarlySnippet(meta, r.AbstractText),
			rank:    len(results) + 1,
		})
	}
	return results, nil
}

// ----------------------------------------------------------------- Crossref

type crossrefSource struct{}

func (crossrefSource) name() string { return "crossref" }
func (crossrefSource) optIn() bool  { return true }

func (crossrefSource) search(ctx context.Context, f *fetcher, query string, limit int) ([]sourceResult, error) {
	if limit <= 0 {
		limit = defaultLimit
	}
	endpoint := url.URL{Scheme: "https", Host: "api.crossref.org", Path: "/works"}
	params := endpoint.Query()
	params.Set("query", query)
	// Unfiltered, relevance surfaces books and chapters ahead of papers.
	params.Set("filter", "type:journal-article")
	params.Set("rows", strconv.Itoa(min(limit, 100)))
	params.Set("select", "DOI,title,container-title,published,is-referenced-by-count,abstract")
	endpoint.RawQuery = params.Encode()
	body, err := f.get(ctx, endpoint.String(), "application/json", []string{"application/json"})
	if err != nil {
		return nil, err
	}
	return parseCrossrefResults(body, limit)
}

type crossrefResponse struct {
	Message struct {
		Items []struct {
			DOI            string   `json:"DOI"`
			Title          []string `json:"title"`
			ContainerTitle []string `json:"container-title"`
			Published      struct {
				DateParts [][]int `json:"date-parts"`
			} `json:"published"`
			ReferencedBy int    `json:"is-referenced-by-count"`
			Abstract     string `json:"abstract"`
		} `json:"items"`
	} `json:"message"`
}

func parseCrossrefResults(body []byte, limit int) ([]sourceResult, error) {
	var payload crossrefResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode crossref response: %w", err)
	}
	items := payload.Message.Items
	results := make([]sourceResult, 0, min(limit, len(items)))
	for _, it := range items {
		if len(results) >= limit {
			break
		}
		link := doiURL(it.DOI)
		if link == "" || len(it.Title) == 0 {
			continue
		}
		title := sanitizeSnippet(it.Title[0])
		if title == "" {
			continue
		}
		meta := scholarlyMeta{citations: it.ReferencedBy}
		if len(it.ContainerTitle) > 0 {
			meta.venue = it.ContainerTitle[0]
		}
		if dp := it.Published.DateParts; len(dp) > 0 && len(dp[0]) > 0 {
			meta.year = dp[0][0]
		}
		// Crossref abstracts are JATS XML (<jats:p>…); sanitizeSnippet
		// strips the tags.
		results = append(results, sourceResult{
			title:   title,
			url:     link,
			snippet: scholarlySnippet(meta, strings.TrimPrefix(sanitizeSnippet(it.Abstract), "Abstract ")),
			rank:    len(results) + 1,
		})
	}
	return results, nil
}

// --------------------------------------------------------- Semantic Scholar

// semanticScholarSource is registered only with an API key: the keyless
// pool is shared by every anonymous client and answers 429 most of the time.
type semanticScholarSource struct{ apiKey string }

func (semanticScholarSource) name() string { return "semanticscholar" }
func (semanticScholarSource) optIn() bool  { return true }

func (s semanticScholarSource) search(ctx context.Context, f *fetcher, query string, limit int) ([]sourceResult, error) {
	if limit <= 0 {
		limit = defaultLimit
	}
	endpoint := url.URL{Scheme: "https", Host: "api.semanticscholar.org", Path: "/graph/v1/paper/search"}
	params := endpoint.Query()
	params.Set("query", query)
	params.Set("limit", strconv.Itoa(min(limit, 100)))
	params.Set("fields", "title,url,abstract,year,venue,citationCount,tldr,externalIds,openAccessPdf")
	endpoint.RawQuery = params.Encode()
	headers := http.Header{"X-Api-Key": []string{s.apiKey}}
	body, err := f.getWithHeaders(ctx, endpoint.String(), "application/json", []string{"application/json"}, headers)
	if err != nil {
		return nil, redactErrorSecret(err, s.apiKey)
	}
	return parseSemanticScholarResults(body, limit)
}

type semanticScholarResponse struct {
	Data []struct {
		Title         string                 `json:"title"`
		URL           string                 `json:"url"`
		Abstract      string                 `json:"abstract"`
		Year          int                    `json:"year"`
		Venue         string                 `json:"venue"`
		CitationCount int                    `json:"citationCount"`
		ExternalIDs   map[string]any         `json:"externalIds"`
		TLDR          *struct{ Text string } `json:"tldr"`
		OpenAccessPDF *struct {
			URL string `json:"url"`
		} `json:"openAccessPdf"`
	} `json:"data"`
}

func parseSemanticScholarResults(body []byte, limit int) ([]sourceResult, error) {
	var payload semanticScholarResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode semanticscholar response: %w", err)
	}
	results := make([]sourceResult, 0, min(limit, len(payload.Data)))
	for _, p := range payload.Data {
		if len(results) >= limit {
			break
		}
		title := collapseWhitespace(p.Title)
		link := ""
		if doi, ok := p.ExternalIDs["DOI"].(string); ok {
			link = doiURL(doi)
		}
		if link == "" && isSafeResultURL(p.URL) {
			link = p.URL
		}
		if title == "" || link == "" {
			continue
		}
		meta := scholarlyMeta{year: p.Year, venue: p.Venue, citations: p.CitationCount}
		if p.OpenAccessPDF != nil && isSafeResultURL(p.OpenAccessPDF.URL) {
			meta.pdf = p.OpenAccessPDF.URL
		}
		// The TLDR is a one-sentence model summary — denser than the
		// abstract's opening for a snippet.
		abstract := p.Abstract
		if p.TLDR != nil && strings.TrimSpace(p.TLDR.Text) != "" {
			abstract = "TL;DR: " + p.TLDR.Text
		}
		results = append(results, sourceResult{
			title:   title,
			url:     link,
			snippet: scholarlySnippet(meta, abstract),
			rank:    len(results) + 1,
		})
	}
	return results, nil
}
