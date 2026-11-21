package searchwire

import (
	"testing"
)

func TestRelevanceTokens(t *testing.T) {
	got := relevanceTokens("How to cancel Go contexts? (golang) dan yang lain")
	want := []string{"cancel", "go", "context", "golang", "lain"}
	for _, w := range []string{"cancellation", "cancelation", "cancelled", "canceling", "cancels"} {
		if lightStem(w) != "cancel" {
			t.Fatalf("lightStem(%q) = %q", w, lightStem(w))
		}
	}
	if lightStem("class") != "class" || lightStem("go") != "go" || lightStem("kebijakan") != "kebijakan" {
		t.Fatal("lightStem over-stemmed")
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestTermMatches(t *testing.T) {
	for _, tc := range []struct {
		q, d string
		want bool
	}{
		{"cancel", "cancellation", true},
		{"cancellation", "cancel", true},
		{"context", "context", true},
		{"go", "golang", false}, // too short for prefix matching
		{"postgres", "postgresql", true},
		{lightStem("general"), lightStem("generation"), false},
	} {
		if got := termMatches(tc.q, tc.d); got != tc.want {
			t.Fatalf("termMatches(%q, %q) = %v", tc.q, tc.d, got)
		}
	}
}

// The motivating case: a keyless source's off-topic top hit used to tie
// with on-topic hits at score 1.0.
func TestFuseResultsDemotesOffTopicKeylessHits(t *testing.T) {
	sources := []source{wikipediaSource{}, githubSource{}}
	outcomes := []sourceOutcome{
		{name: "wikipedia", results: []sourceResult{
			{title: "Thread pool", url: "https://en.wikipedia.org/wiki/Thread_pool", snippet: "A software design pattern for achieving concurrency of execution.", rank: 1},
		}},
		{name: "github", results: []sourceResult{
			{title: "blog-example-go-context-cancellation", url: "https://github.com/x/blog-example-go-context-cancellation", snippet: "Example for golang context cancellation", rank: 1},
			{title: "rocketlaunchr/mysql-go", url: "https://github.com/rocketlaunchr/mysql-go", snippet: "Make mysql cancelation a breeze", rank: 2},
		}},
	}
	got := fuseResults("golang context cancellation", nil, outcomes, sources, 10)
	if len(got) != 3 {
		t.Fatalf("results = %+v", got)
	}
	if got[0].Title != "blog-example-go-context-cancellation" || got[0].Score != 1 {
		t.Fatalf("top = %+v", got[0])
	}
	if got[1].Title != "rocketlaunchr/mysql-go" || got[2].Title != "Thread pool" || got[2].Score >= 0.5 {
		t.Fatalf("off-topic hit not demoted: %+v", got)
	}
}

func TestFuseResultsKeepsTrustedRanking(t *testing.T) {
	sources := []source{serperSource{apiKey: "k"}, wikipediaSource{}}
	outcomes := []sourceOutcome{
		{name: "serper", results: []sourceResult{{title: "Unrelated words", url: "https://a.example/1", rank: 1}}},
		{name: "wikipedia", results: []sourceResult{{title: "Unrelated too", url: "https://b.example/2", rank: 1}}},
	}
	got := fuseResults("golang context", nil, outcomes, sources, 10)
	if got[0].URL != "https://a.example/1" || got[0].Score != 1 || got[1].Score >= 0.5 {
		t.Fatalf("trusted source should keep its fusion score: %+v", got)
	}
}

func TestFuseResultsStopwordOnlyQueryIsUnchanged(t *testing.T) {
	sources := []source{wikipediaSource{}}
	outcomes := []sourceOutcome{{name: "wikipedia", results: []sourceResult{
		{title: "A", url: "https://a.example/", rank: 1},
		{title: "B", url: "https://b.example/", rank: 2},
	}}}
	plain := fuseResults("", nil, outcomes, sources, 10)
	stop := fuseResults("what is the", nil, outcomes, sources, 10)
	for i := range plain {
		if plain[i].URL != stop[i].URL || plain[i].Score != stop[i].Score {
			t.Fatalf("plain = %+v stop = %+v", plain, stop)
		}
	}
}
