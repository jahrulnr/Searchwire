package searchwire

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tinyModel is a 4-dimensional static model: "abort" is a near-synonym of
// "cancel", "banana" is orthogonal to both.
var tinyModel = []struct {
	token string
	vec   [4]float32
}{
	{"[UNK]", [4]float32{0, 0, 0, 0}},
	{"cancel", [4]float32{1, 0, 0, 0}},
	{"abort", [4]float32{0.95, 0.05, 0, 0}},
	{"banana", [4]float32{0, 0, 1, 0}},
	{"go", [4]float32{0, 1, 0, 0}},
	{"##lang", [4]float32{0, 1, 0, 0}},
	{"context", [4]float32{0, 0, 0, 1}},
	{"!", [4]float32{0, 0, 1, 1}},
	{"cafe", [4]float32{0, 0, 0.5, 0.5}},
}

// writeTinyModel writes a model2vec-format directory; dtype is F32 or F16.
func writeTinyModel(t *testing.T, dtype string) string {
	t.Helper()
	dir := t.TempDir()
	vocab := map[string]int{}
	for i, e := range tinyModel {
		vocab[e.token] = i
	}
	tok, _ := json.Marshal(map[string]any{
		"normalizer": map[string]any{"type": "BertNormalizer", "lowercase": true},
		"model": map[string]any{
			"type": "WordPiece", "unk_token": "[UNK]", "continuing_subword_prefix": "##",
			"max_input_chars_per_word": 100, "vocab": vocab,
		},
	})
	var data []byte
	for _, e := range tinyModel {
		for _, v := range e.vec {
			if dtype == "F16" {
				data = binary.LittleEndian.AppendUint16(data, float32ToFloat16(v))
			} else {
				data = binary.LittleEndian.AppendUint32(data, math.Float32bits(v))
			}
		}
	}
	header, _ := json.Marshal(map[string]any{"embeddings": map[string]any{
		"dtype": dtype, "shape": []int{len(tinyModel), 4}, "data_offsets": []int{0, len(data)},
	}})
	st := binary.LittleEndian.AppendUint64(nil, uint64(len(header)))
	st = append(append(st, header...), data...)
	for name, content := range map[string][]byte{
		"tokenizer.json": tok, "model.safetensors": st, "config.json": []byte(`{"normalize": true}`),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// float32ToFloat16 handles the exact values the tiny model uses.
func float32ToFloat16(f float32) uint16 {
	b := math.Float32bits(f)
	sign := uint16(b>>16) & 0x8000
	exp := int((b>>23)&0xff) - 127 + 15
	if f == 0 {
		return sign
	}
	return sign | uint16(exp)<<10 | uint16((b>>13)&0x3ff)
}

func TestStaticEmbedderTokenizes(t *testing.T) {
	e, err := loadStaticEmbedder(writeTinyModel(t, "F32"))
	if err != nil {
		t.Fatal(err)
	}
	var got []int32
	for _, w := range e.preTokenize(e.normalizeText("GoLang  Café!\tzzz")) {
		got = append(got, e.wordPiece(w)...)
	}
	// go ##lang | cafe (accent stripped) | ! (punctuation split) | [UNK]
	want := []int32{4, 5, 8, 7, 0}
	if len(got) != len(want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ids = %v, want %v", got, want)
		}
	}
}

// Regression: Go's unicode.IsControl covers only Cc, but the HF normalizer
// deletes every C* rune; a zero-width space used to glue two words into one
// unknown token.
func TestStaticEmbedderDropsFormatCharacters(t *testing.T) {
	e, err := loadStaticEmbedder(writeTinyModel(t, "F32"))
	if err != nil {
		t.Fatal(err)
	}
	got := e.tokenize("go\u200blang \ufeffcontext\u00ad")
	if len(got) != 3 || got[0] != 4 || got[1] != 5 || got[2] != 6 {
		t.Fatalf("ids = %v, want [go ##lang context]", got)
	}
}

// Regression: special tokens written verbatim in text map to their ids
// before normalization, as in the HF tokenizer.
func TestStaticEmbedderMatchesSpecialTokens(t *testing.T) {
	dir := writeTinyModel(t, "F32")
	path := filepath.Join(dir, "tokenizer.json")
	raw, _ := os.ReadFile(path)
	var tok map[string]any
	json.Unmarshal(raw, &tok)
	tok["added_tokens"] = []map[string]any{{"id": 0, "content": "[UNK]", "normalized": false}}
	raw, _ = json.Marshal(tok)
	os.WriteFile(path, raw, 0o644)
	e, err := loadStaticEmbedder(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := e.tokenize("cancel[UNK]context"); len(got) != 3 || got[0] != 1 || got[1] != 0 || got[2] != 6 {
		t.Fatalf("ids = %v, want [cancel [UNK] context]", got)
	}
}

// TestStaticEmbedderGoldenParity compares tokenization with ids produced by
// the reference Hugging Face tokenizers library. It runs only when
// SEARCHWIRE_EMBEDDING_MODEL points at minishlab/potion-base-8M.
func TestStaticEmbedderGoldenParity(t *testing.T) {
	dir := os.Getenv("SEARCHWIRE_EMBEDDING_MODEL")
	if dir == "" {
		t.Skip("SEARCHWIRE_EMBEDDING_MODEL not set")
	}
	var golden struct {
		Cases []struct {
			Text string  `json:"text"`
			IDs  []int32 `json:"ids"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(readFixture(t, "potion-base-8M-tokenizer-golden.json"), &golden); err != nil {
		t.Fatal(err)
	}
	e, err := loadStaticEmbedder(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.vocab) != 29528 {
		t.Skipf("model at %s is not potion-base-8M (vocab %d)", dir, len(e.vocab))
	}
	for _, c := range golden.Cases {
		got := e.tokenize(c.Text)
		if len(got) != len(c.IDs) {
			t.Fatalf("%q: ids = %v, want %v", c.Text, got, c.IDs)
		}
		for i := range got {
			if got[i] != c.IDs[i] {
				t.Fatalf("%q: ids = %v, want %v", c.Text, got, c.IDs)
			}
		}
	}
}

func TestStaticEmbedderSimilarity(t *testing.T) {
	for _, dtype := range []string{"F32", "F16"} {
		e, err := loadStaticEmbedder(writeTinyModel(t, dtype))
		if err != nil {
			t.Fatalf("%s: %v", dtype, err)
		}
		q := e.embed("cancel")
		if c := cosine(q, e.embed("abort")); c < 0.99 {
			t.Fatalf("%s: cos(cancel, abort) = %v", dtype, c)
		}
		if c := cosine(q, e.embed("banana")); c > 0.01 {
			t.Fatalf("%s: cos(cancel, banana) = %v", dtype, c)
		}
		if e.embed("zzz qqq") != nil {
			t.Fatalf("%s: all-unknown text should embed to nil", dtype)
		}
	}
}

func TestStaticEmbedderRejectsBadModels(t *testing.T) {
	if _, err := loadStaticEmbedder(t.TempDir()); err == nil {
		t.Fatal("empty dir loaded")
	}
	dir := writeTinyModel(t, "F32")
	os.WriteFile(filepath.Join(dir, "tokenizer.json"), []byte(`{"model":{"type":"BPE","vocab":{"a":0}}}`), 0o644)
	if _, err := loadStaticEmbedder(dir); err == nil || !strings.Contains(err.Error(), "WordPiece") {
		t.Fatalf("err = %v", err)
	}
	dir = writeTinyModel(t, "F32")
	os.WriteFile(filepath.Join(dir, "model.safetensors"), []byte{1, 0, 0, 0, 0, 0, 0, 0, '{'}, 0o644)
	if _, err := loadStaticEmbedder(dir); err == nil {
		t.Fatal("corrupt safetensors loaded")
	}
}

func TestFuseResultsSemanticBoostsSynonyms(t *testing.T) {
	e, err := loadStaticEmbedder(writeTinyModel(t, "F32"))
	if err != nil {
		t.Fatal(err)
	}
	sources := []source{wikipediaSource{}}
	outcomes := []sourceOutcome{{name: "wikipedia", results: []sourceResult{
		{title: "banana", url: "https://a.example/banana", rank: 1},
		{title: "abort", url: "https://a.example/abort", rank: 2},
	}}}
	lexical := fuseResults("cancel", nil, outcomes, sources, 10)
	if lexical[0].Title != "banana" {
		t.Fatalf("without embeddings rank order should hold: %+v", lexical)
	}
	semantic := fuseResults("cancel", e, outcomes, sources, 10)
	if semantic[0].Title != "abort" || semantic[1].Score >= 0.5 {
		t.Fatalf("synonym not boosted: %+v", semantic)
	}
}

func TestSearcherEmbeddingConfig(t *testing.T) {
	t.Setenv("SEARCHWIRE_EMBEDDING_MODEL", "")
	if New(Config{}).embedder != nil {
		t.Fatal("embedder loaded without config")
	}
	dir := writeTinyModel(t, "F32")
	if s := New(Config{Embedding: EmbeddingConfig{ModelDir: dir}}); s.embedder == nil || s.embeddingErr != nil {
		t.Fatalf("embedder = %v err = %v", s.embedder, s.embeddingErr)
	}
	t.Setenv("SEARCHWIRE_EMBEDDING_MODEL", dir)
	if New(Config{}).embedder == nil {
		t.Fatal("env model not loaded")
	}

	s := newSearcherWithSources([]source{fakeSource{nameValue: "fast", searchFn: okSource("https://example.com/fast")}}, Config{})
	s.embeddingErr = New(Config{Embedding: EmbeddingConfig{ModelDir: filepath.Join(t.TempDir(), "missing")}}).embeddingErr
	if s.embeddingErr == nil {
		t.Fatal("missing model dir should record an error")
	}
	resp, err := s.SearchWithOptions(context.Background(), "q", SearchOptions{})
	if err != nil || len(resp.Results) != 1 || len(resp.Warnings) != 1 || !strings.Contains(resp.Warnings[0], "using lexical relevance only") {
		t.Fatalf("resp = %+v err = %v", resp, err)
	}
}

func TestFloat16ToFloat32(t *testing.T) {
	for _, tc := range []struct {
		h    uint16
		want float32
	}{
		{0x3c00, 1}, {0xc000, -2}, {0x3800, 0.5}, {0x0000, 0}, {0x0001, 5.960464477539063e-08},
	} {
		if got := float16ToFloat32(tc.h); got != tc.want {
			t.Fatalf("f16 %#04x = %v, want %v", tc.h, got, tc.want)
		}
	}
}
