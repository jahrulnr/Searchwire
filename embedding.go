package searchwire

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Static embeddings (model2vec "potion" format) for semantic reranking.
//
// A static model maps each WordPiece token to a fixed vector; a text's
// embedding is the mean of its token vectors, L2-normalized. There is no
// neural network at query time, so embedding a search result costs a few
// microseconds in pure Go. The model directory holds the three files a
// model2vec export ships:
//
//	model.safetensors  tensor "embeddings", F32 or F16, shape [vocab, dim]
//	tokenizer.json     Hugging Face tokenizer with a BERT WordPiece model
//	config.json        {"normalize": true, ...}
//
// e.g. https://huggingface.co/minishlab/potion-base-8M (MIT, ~31 MB).

// embeddingMaxRunes bounds the text embedded per result: title plus the
// start of the snippet carries the topic, and the cost stays flat.
const embeddingMaxRunes = 600

type staticEmbedder struct {
	vocab map[string]int32
	// specials are added tokens matched verbatim before normalization, as
	// the HF tokenizer does ("[CLS]" in text maps to its id).
	specials  []specialToken
	vecs      []float32 // row-major [vocab][dim]
	dim       int
	unk       int32
	maxChars  int
	lowercase bool
	normalize bool
}

type specialToken struct {
	content string
	id      int32
}

var embedderCache sync.Map // abs dir → *embedderLoad

type embedderLoad struct {
	once sync.Once
	emb  *staticEmbedder
	err  error
}

// loadStaticEmbedderCached loads a model directory once per process; New
// may be called many times with the same config.
func loadStaticEmbedderCached(dir string) (*staticEmbedder, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	v, _ := embedderCache.LoadOrStore(abs, &embedderLoad{})
	l := v.(*embedderLoad)
	l.once.Do(func() { l.emb, l.err = loadStaticEmbedder(abs) })
	return l.emb, l.err
}

func loadStaticEmbedder(dir string) (*staticEmbedder, error) {
	e := &staticEmbedder{unk: -1, maxChars: 100, lowercase: true, normalize: true}
	if raw, err := os.ReadFile(filepath.Join(dir, "config.json")); err == nil {
		var cfg struct {
			Normalize *bool `json:"normalize"`
		}
		if json.Unmarshal(raw, &cfg) == nil && cfg.Normalize != nil {
			e.normalize = *cfg.Normalize
		}
	}
	if err := e.loadTokenizer(filepath.Join(dir, "tokenizer.json")); err != nil {
		return nil, err
	}
	if err := e.loadSafetensors(filepath.Join(dir, "model.safetensors")); err != nil {
		return nil, err
	}
	return e, nil
}

func (e *staticEmbedder) loadTokenizer(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read tokenizer: %w", err)
	}
	var tok struct {
		AddedTokens []struct {
			ID         int32  `json:"id"`
			Content    string `json:"content"`
			Normalized bool   `json:"normalized"`
		} `json:"added_tokens"`
		Normalizer *struct {
			Type      string `json:"type"`
			Lowercase *bool  `json:"lowercase"`
		} `json:"normalizer"`
		Model struct {
			Type                    string           `json:"type"`
			UnkToken                string           `json:"unk_token"`
			ContinuingSubwordPrefix string           `json:"continuing_subword_prefix"`
			MaxInputCharsPerWord    int              `json:"max_input_chars_per_word"`
			Vocab                   map[string]int32 `json:"vocab"`
		} `json:"model"`
	}
	if err := json.Unmarshal(raw, &tok); err != nil {
		return fmt.Errorf("decode tokenizer: %w", err)
	}
	if tok.Model.Type != "WordPiece" {
		return fmt.Errorf("unsupported tokenizer model %q (want WordPiece)", tok.Model.Type)
	}
	if p := tok.Model.ContinuingSubwordPrefix; p != "" && p != "##" {
		return fmt.Errorf("unsupported subword prefix %q", p)
	}
	if len(tok.Model.Vocab) == 0 {
		return errors.New("tokenizer vocab is empty")
	}
	if tok.Normalizer != nil && tok.Normalizer.Lowercase != nil {
		e.lowercase = *tok.Normalizer.Lowercase
	}
	if tok.Model.MaxInputCharsPerWord > 0 {
		e.maxChars = tok.Model.MaxInputCharsPerWord
	}
	e.vocab = tok.Model.Vocab
	for _, at := range tok.AddedTokens {
		if at.Content != "" && !at.Normalized {
			e.specials = append(e.specials, specialToken{at.Content, at.ID})
		}
	}
	if id, ok := e.vocab[tok.Model.UnkToken]; ok {
		e.unk = id
	}
	return nil
}

func (e *staticEmbedder) loadSafetensors(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read model: %w", err)
	}
	if len(raw) < 8 {
		return errors.New("model.safetensors is truncated")
	}
	n := binary.LittleEndian.Uint64(raw[:8])
	if n > uint64(len(raw)-8) {
		return errors.New("model.safetensors header exceeds file")
	}
	var header map[string]json.RawMessage
	if err := json.Unmarshal(raw[8:8+n], &header); err != nil {
		return fmt.Errorf("decode safetensors header: %w", err)
	}
	var t struct {
		Dtype       string `json:"dtype"`
		Shape       []int  `json:"shape"`
		DataOffsets [2]int `json:"data_offsets"`
	}
	entry, ok := header["embeddings"]
	if !ok {
		return errors.New(`model.safetensors has no "embeddings" tensor`)
	}
	if err := json.Unmarshal(entry, &t); err != nil {
		return fmt.Errorf("decode embeddings tensor: %w", err)
	}
	if len(t.Shape) != 2 || t.Shape[0] <= 0 || t.Shape[1] <= 0 {
		return fmt.Errorf("embeddings shape %v is not [vocab, dim]", t.Shape)
	}
	rows, dim := t.Shape[0], t.Shape[1]
	data := raw[8+n:]
	start, end := t.DataOffsets[0], t.DataOffsets[1]
	if start < 0 || end > len(data) || start > end {
		return errors.New("embeddings data offsets out of range")
	}
	data = data[start:end]
	count := rows * dim
	vecs := make([]float32, count)
	switch t.Dtype {
	case "F32":
		if len(data) != count*4 {
			return fmt.Errorf("embeddings size %d != %d", len(data), count*4)
		}
		for i := range vecs {
			vecs[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
		}
	case "F16":
		if len(data) != count*2 {
			return fmt.Errorf("embeddings size %d != %d", len(data), count*2)
		}
		for i := range vecs {
			vecs[i] = float16ToFloat32(binary.LittleEndian.Uint16(data[i*2:]))
		}
	default:
		return fmt.Errorf("unsupported embeddings dtype %q (want F32 or F16)", t.Dtype)
	}
	for word, id := range e.vocab {
		if id < 0 || int(id) >= rows {
			return fmt.Errorf("token %q id %d outside embeddings (%d rows)", word, id, rows)
		}
	}
	e.vecs, e.dim = vecs, dim
	return nil
}

func float16ToFloat32(h uint16) float32 {
	sign := uint32(h>>15) << 31
	exp := uint32(h>>10) & 0x1f
	frac := uint32(h) & 0x3ff
	switch {
	case exp == 0 && frac == 0:
		return math.Float32frombits(sign)
	case exp == 0: // subnormal
		for frac&0x400 == 0 {
			frac <<= 1
			exp--
		}
		exp++
		frac &= 0x3ff
	case exp == 0x1f:
		return math.Float32frombits(sign | 0x7f800000 | frac<<13)
	}
	return math.Float32frombits(sign | (exp+112)<<23 | frac<<13)
}

// embed returns the L2-normalized mean token vector of text, or nil when
// no token is in the vocabulary.
func (e *staticEmbedder) embed(text string) []float32 {
	if r := []rune(text); len(r) > embeddingMaxRunes {
		text = string(r[:embeddingMaxRunes])
	}
	sum := make([]float32, e.dim)
	n := 0
	for _, id := range e.tokenize(text) {
		if id == e.unk {
			continue // model2vec drops unknown tokens
		}
		row := e.vecs[int(id)*e.dim : int(id+1)*e.dim]
		for i, v := range row {
			sum[i] += v
		}
		n++
	}
	if n == 0 {
		return nil
	}
	var norm float64
	for i := range sum {
		sum[i] /= float32(n)
		norm += float64(sum[i]) * float64(sum[i])
	}
	if norm == 0 {
		return nil
	}
	inv := float32(1 / math.Sqrt(norm))
	for i := range sum {
		sum[i] *= inv
	}
	return sum
}

// tokenize returns WordPiece ids for text without [CLS]/[SEP], matching the
// HF tokenizer's encode(text, add_special_tokens=False): verbatim special
// tokens are split out first, the rest is normalized, pre-tokenized, and
// word-pieced.
func (e *staticEmbedder) tokenize(text string) []int32 {
	var ids []int32
	for text != "" {
		at, sp := -1, specialToken{}
		for _, cand := range e.specials {
			if i := strings.Index(text, cand.content); i >= 0 && (at < 0 || i < at || i == at && len(cand.content) > len(sp.content)) {
				at, sp = i, cand
			}
		}
		chunk := text
		if at >= 0 {
			chunk = text[:at]
		}
		for _, word := range e.preTokenize(e.normalizeText(chunk)) {
			ids = append(ids, e.wordPiece(word)...)
		}
		if at < 0 {
			break
		}
		ids = append(ids, sp.id)
		text = text[at+len(sp.content):]
	}
	return ids
}

func cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	return dot // inputs are unit vectors
}

// normalizeText mirrors the Hugging Face BertNormalizer: drop control
// characters, map whitespace to spaces, pad CJK ideographs with spaces,
// and (when lowercasing) strip accents and lowercase.
func (e *staticEmbedder) normalizeText(s string) string {
	if e.lowercase {
		s = norm.NFD.String(s)
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == 0 || r == 0xfffd || (isOtherCategory(r) && r != '\t' && r != '\n' && r != '\r'):
			continue
		case unicode.IsSpace(r):
			b.WriteByte(' ')
		case isCJKIdeograph(r):
			b.WriteByte(' ')
			b.WriteRune(r)
			b.WriteByte(' ')
		case e.lowercase && unicode.Is(unicode.Mn, r):
			continue
		case e.lowercase:
			b.WriteRune(unicode.ToLower(r))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// isOtherCategory reports Unicode category C* (control, format, surrogate,
// private use, unassigned), which the HF BertNormalizer deletes. Go's
// unicode.IsControl covers only Cc, so zero-width spaces and BOMs inside
// scraped text would otherwise glue words into one unknown token.
func isOtherCategory(r rune) bool {
	if unicode.In(r, unicode.Cc, unicode.Cf, unicode.Co, unicode.Cs) {
		return true
	}
	return !unicode.In(r, unicode.L, unicode.M, unicode.N, unicode.P, unicode.S, unicode.Z)
}

func isCJKIdeograph(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0x3400 && r <= 0x4DBF) ||
		(r >= 0x20000 && r <= 0x2A6DF) || (r >= 0x2A700 && r <= 0x2B73F) ||
		(r >= 0x2B740 && r <= 0x2B81F) || (r >= 0x2B820 && r <= 0x2CEAF) ||
		(r >= 0xF900 && r <= 0xFAFF) || (r >= 0x2F800 && r <= 0x2FA1F)
}

// isBertPunct matches BERT's punctuation rule: ASCII symbol ranges count
// even where Unicode classes them as symbols ($, ^, `).
func isBertPunct(r rune) bool {
	if (r >= 33 && r <= 47) || (r >= 58 && r <= 64) || (r >= 91 && r <= 96) || (r >= 123 && r <= 126) {
		return true
	}
	return unicode.IsPunct(r)
}

// preTokenize mirrors BertPreTokenizer: split on whitespace and isolate
// each punctuation character.
func (e *staticEmbedder) preTokenize(s string) []string {
	var words []string
	start := -1
	for i, r := range s {
		switch {
		case unicode.IsSpace(r):
			if start >= 0 {
				words = append(words, s[start:i])
				start = -1
			}
		case isBertPunct(r):
			if start >= 0 {
				words = append(words, s[start:i])
				start = -1
			}
			words = append(words, string(r))
		default:
			if start < 0 {
				start = i
			}
		}
	}
	if start >= 0 {
		words = append(words, s[start:])
	}
	return words
}

// wordPiece splits one pre-token greedily into the longest vocabulary
// pieces, continuation pieces carrying the "##" prefix. A word that cannot
// be fully covered (or is too long) becomes a single unknown token.
func (e *staticEmbedder) wordPiece(word string) []int32 {
	if len([]rune(word)) > e.maxChars {
		return []int32{e.unk}
	}
	var ids []int32
	for start := 0; start < len(word); {
		end := len(word)
		found := int32(-1)
		for end > start {
			piece := word[start:end]
			if start > 0 {
				piece = "##" + piece
			}
			if id, ok := e.vocab[piece]; ok {
				found = id
				break
			}
			// Step back one rune, not one byte.
			for end--; end > start && !utf8RuneStart(word[end]); end-- {
			}
		}
		if found < 0 {
			return []int32{e.unk}
		}
		ids = append(ids, found)
		start = end
	}
	return ids
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }
