package searchwire

import (
	"math"
	"net/url"
	"strings"
	"unicode"
)

// Lexical relevance rerank.
//
// Reciprocal-rank fusion only knows where each source placed a hit, not what
// the hit says, so a keyless scraper's off-topic result (a Wikipedia article
// sharing no word with the query) can tie with the best match. This pass
// scores every fused candidate by how much of the query its title, snippet,
// and URL actually cover, weighting rare query terms above common ones
// (IDF over the candidate set), and scales the fusion score by it:
//
//	score = rrf * (relevanceFloor + (1-relevanceFloor) * coverage)
//
// With a static embedding model configured (Config.Embedding), semantic
// similarity fills the gap lexical matching leaves — synonyms and
// paraphrases — but never lowers a lexical match:
//
//	coverage = lexical + (1-lexical) * semantic
//
// Hits from sources that rank with their own relevance model (API-backed
// search such as Brave API, Serper, Tavily, Semantic Scholar) keep their
// fusion score untouched. Nothing is dropped: off-topic hits sink and fall
// past the limit when enough on-topic hits exist.

// relevanceFloor is the share of the fusion score a hit keeps even when it
// matches no query term, so a source's ranking still counts for queries the
// tokenizer handles poorly (unsegmented scripts, heavy synonyms).
const relevanceFloor = 0.3

// Cosine similarity from static embeddings is mapped onto [0,1]:
// unrelated text sits around 0.0–0.15, a close paraphrase above 0.6.
const (
	semanticCosineFloor = 0.15
	semanticCosineCeil  = 0.60
)

func semanticScore(cos float64) float64 {
	v := (cos - semanticCosineFloor) / (semanticCosineCeil - semanticCosineFloor)
	return math.Max(0, math.Min(1, v))
}

// trustedRankingSource marks a source whose ranking already reflects query
// relevance (a search API with its own retrieval model).
type trustedRankingSource interface {
	trustedRanking() bool
}

func hasTrustedRanking(src source) bool {
	t, ok := src.(trustedRankingSource)
	return ok && t.trustedRanking()
}

func (b braveSource) trustedRanking() bool         { return b.apiKey != "" }
func (serperSource) trustedRanking() bool          { return true }
func (tavilySource) trustedRanking() bool          { return true }
func (semanticScholarSource) trustedRanking() bool { return true }

// relevanceStopwords are dropped from queries and documents: they match
// everywhere and carry no topic. English plus common Indonesian.
var relevanceStopwords = func() map[string]bool {
	words := strings.Fields(`a an and are as at be by for from has have how i in is it its of on or
		that the this to was what when where which who why will with you your do does can vs via
		yang dan di ke dari untuk dengan pada ini itu atau adalah dalam cara apa bagaimana tidak`)
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}()

// relevanceTokens lowercases s and splits it into letter/digit runs,
// dropping stopwords and single characters, and folding a plural "s".
func relevanceTokens(s string) []string {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := fields[:0]
	for _, f := range fields {
		if len([]rune(f)) < 2 || relevanceStopwords[f] {
			continue
		}
		out = append(out, lightStem(f))
	}
	return out
}

// stemSuffixes are stripped longest-first by lightStem.
var stemSuffixes = []string{"ations", "ation", "ments", "ment", "ings", "ing", "ions", "ion", "ers", "er", "ed", "ly", "es", "s"}

// lightStem strips one common English inflectional suffix and undoubles a
// trailing consonant so spelling variants meet: cancellation, cancelation,
// cancelled, canceling → cancel. It is deliberately conservative: stems
// shorter than 3 characters are left alone, and non-ASCII words pass
// through unchanged.
func lightStem(w string) string {
	for _, r := range w {
		if r > unicode.MaxASCII {
			return w
		}
	}
	for _, suf := range stemSuffixes {
		if !strings.HasSuffix(w, suf) || len(w)-len(suf) < 3 {
			continue
		}
		if suf == "s" && strings.HasSuffix(w, "ss") {
			break
		}
		stem := w[:len(w)-len(suf)]
		if n := len(stem); n >= 4 && stem[n-1] == stem[n-2] && !strings.ContainsRune("aeiouy", rune(stem[n-1])) {
			stem = stem[:n-1]
		}
		return stem
	}
	return w
}

// termMatches reports whether query term q matches document token d (both
// already stemmed): equal, or one is a prefix of the other with the shorter
// at least 6 characters ("postgres" ~ "postgresql"). The length floor keeps
// short stems from over-matching ("gener" from generation vs "general").
func termMatches(q, d string) bool {
	if q == d {
		return true
	}
	short, long := q, d
	if len(short) > len(long) {
		short, long = long, short
	}
	return len(short) >= 6 && strings.HasPrefix(long, short)
}

func containsTerm(tokens []string, term string) bool {
	for _, t := range tokens {
		if termMatches(term, t) {
			return true
		}
	}
	return false
}

// urlTokens extracts host and path words; slugs often name the topic when
// a snippet is empty.
func urlTokens(raw string) []string {
	u, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	return relevanceTokens(u.Host + " " + u.Path)
}

// applyRelevance rescales candidate scores in place by lexical query
// coverage. It is a no-op for queries with no informative terms.
func applyRelevance(query string, emb *staticEmbedder, candidates []*fusedCandidate, trusted map[string]bool) {
	terms := uniqueStrings(relevanceTokens(query))
	if len(terms) == 0 || len(candidates) == 0 {
		return
	}
	type doc struct{ title, body []string }
	docs := make([]doc, len(candidates))
	df := make([]int, len(terms))
	for i, c := range candidates {
		docs[i] = doc{
			title: relevanceTokens(c.title),
			body:  append(relevanceTokens(c.snippet), urlTokens(c.url)...),
		}
		for j, t := range terms {
			if containsTerm(docs[i].title, t) || containsTerm(docs[i].body, t) {
				df[j]++
			}
		}
	}
	n := float64(len(candidates))
	idf := make([]float64, len(terms))
	var total float64
	for j := range terms {
		idf[j] = math.Log(1 + (n-float64(df[j])+0.5)/(float64(df[j])+0.5))
		total += idf[j]
	}
	if total == 0 {
		return
	}
	var qvec []float32
	if emb != nil {
		qvec = emb.embed(query)
	}
	for i, c := range candidates {
		if candidateTrusted(c, trusted) {
			continue
		}
		var got float64
		for j, t := range terms {
			switch {
			case containsTerm(docs[i].title, t):
				got += idf[j]
			case containsTerm(docs[i].body, t):
				// A term only in the snippet/URL is weaker evidence than
				// one in the title.
				got += 0.7 * idf[j]
			}
		}
		c.relevance = got / total
		if qvec != nil && c.relevance < 1 {
			if dvec := emb.embed(c.title + ". " + c.snippet); dvec != nil {
				c.relevance += (1 - c.relevance) * semanticScore(cosine(qvec, dvec))
			}
		}
		c.score *= relevanceFloor + (1-relevanceFloor)*c.relevance
	}
}

func candidateTrusted(c *fusedCandidate, trusted map[string]bool) bool {
	for _, s := range c.sources {
		if trusted[s] {
			return true
		}
	}
	return false
}

func uniqueStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
