package searchwire

import (
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
)

type sourceOutcome struct {
	index   int
	name    string
	results []sourceResult
	err     error
}

type fusedCandidate struct {
	title          string
	url            string
	snippet        string
	sources        []string
	score          float64
	sourcePriority int
	bestRank       int
	relevance      float64
}

var trackingParamPrefixes = []string{"utm_", "fbclid", "gclid"}

// fuseResults merges per-source hits by canonical URL with reciprocal-rank
// fusion, then (when query is non-empty) rescales by lexical relevance so
// off-topic hits from keyless sources sink. See applyRelevance.
func fuseResults(query string, emb *staticEmbedder, outcomes []sourceOutcome, sources []source, limit int) []Result {
	byKey := make(map[string]*fusedCandidate)
	sourcePriority := make(map[string]int, len(sources))
	trusted := make(map[string]bool, len(sources))
	for i, src := range sources {
		sourcePriority[src.name()] = i
		trusted[src.name()] = hasTrustedRanking(src)
	}

	for _, outcome := range outcomes {
		srcName := outcome.name
		priority := sourcePriority[srcName]
		for _, item := range outcome.results {
			key, canonical, err := canonicalURL(item.url)
			if err != nil || key == "" {
				continue
			}
			existing, ok := byKey[key]
			score := 1.0 / (rrfK + float64(item.rank))
			if !ok {
				byKey[key] = &fusedCandidate{
					title:          item.title,
					url:            canonical,
					snippet:        item.snippet,
					sources:        []string{srcName},
					score:          score,
					sourcePriority: priority,
					bestRank:       item.rank,
				}
				continue
			}
			existing.score += score
			// Prefer the https form when sources disagree on scheme.
			if strings.HasPrefix(existing.url, "http://") && strings.HasPrefix(canonical, "https://") {
				existing.url = canonical
			}
			if !containsString(existing.sources, srcName) {
				existing.sources = appendSourceInOrder(existing.sources, srcName, sources)
			}
			if len(item.title) > len(existing.title) {
				existing.title = item.title
			}
			if len(item.snippet) > len(existing.snippet) {
				existing.snippet = item.snippet
			}
			if priority < existing.sourcePriority || (priority == existing.sourcePriority && item.rank < existing.bestRank) {
				existing.sourcePriority = priority
				existing.bestRank = item.rank
			}
		}
	}

	candidates := make([]*fusedCandidate, 0, len(byKey))
	for _, candidate := range byKey {
		candidates = append(candidates, candidate)
	}
	applyRelevance(query, emb, candidates, trusted)
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.score != b.score {
			return a.score > b.score
		}
		if a.sourcePriority != b.sourcePriority {
			return a.sourcePriority < b.sourcePriority
		}
		if a.bestRank != b.bestRank {
			return a.bestRank < b.bestRank
		}
		return a.url < b.url
	})

	if limit > len(candidates) {
		limit = len(candidates)
	}
	if limit == 0 {
		return []Result{}
	}
	// Normalise the raw RRF sum against the top candidate so the exposed
	// score reads as a relative rank on a 0..1 scale (top result = 1.0).
	// It is a fusion-rank signal only: multi-source hits score ~2x a
	// single-source hit at the same rank, but it says nothing about how
	// well any result matches the query — a nonsense query still yields
	// a top score of 1.0. Sources lists the corroborating engines; that
	// count is the more meaningful quality hint for a model caller.
	topScore := candidates[0].score
	results := make([]Result, limit)
	for i := 0; i < limit; i++ {
		c := candidates[i]
		results[i] = Result{
			Title:   c.title,
			URL:     c.url,
			Snippet: c.snippet,
			Sources: append([]string(nil), c.sources...),
			Score:   c.score / topScore,
		}
	}
	return results
}

func appendSourceInOrder(existing []string, name string, sources []source) []string {
	if containsString(existing, name) {
		return existing
	}
	order := make(map[string]int, len(sources))
	for i, src := range sources {
		order[src.name()] = i
	}
	existing = append(existing, name)
	sort.Slice(existing, func(i, j int) bool {
		return order[existing[i]] < order[existing[j]]
	})
	return existing
}

func containsString(values []string, target string) bool {
	return slices.Contains(values, target)
}

func canonicalURL(raw string) (key string, canonical string, err error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", "", fmt.Errorf("unsupported scheme")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""

	if u.Scheme == "http" && strings.HasSuffix(u.Host, ":80") {
		u.Host = strings.TrimSuffix(u.Host, ":80")
	}
	if u.Scheme == "https" && strings.HasSuffix(u.Host, ":443") {
		u.Host = strings.TrimSuffix(u.Host, ":443")
	}

	if u.Path == "" {
		u.Path = "/"
	} else if u.Path != "/" && strings.HasSuffix(u.Path, "/") {
		u.Path = strings.TrimRight(u.Path, "/")
	}

	if u.RawQuery != "" {
		values, parseErr := url.ParseQuery(u.RawQuery)
		if parseErr == nil {
			for keyName := range values {
				lower := strings.ToLower(keyName)
				for _, prefix := range trackingParamPrefixes {
					if lower == prefix || strings.HasPrefix(lower, prefix) {
						values.Del(keyName)
						break
					}
				}
			}
			u.RawQuery = values.Encode()
		}
	}

	canonical = u.String()
	// The dedup key folds scheme and a leading "www." so the http/https and
	// www/bare variants of one page fuse into a single hit; the returned
	// canonical URL keeps the form the source reported.
	k := *u
	k.Scheme = "https"
	k.Host = strings.TrimPrefix(k.Host, "www.")
	return k.String(), canonical, nil
}
