package cache

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// Embedder computes dense vector embeddings for semantic cache matching.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// EncodeFloat32s packs a float32 slice into a little-endian byte slice.
func EncodeFloat32s(v []float32) []byte {
	if len(v) == 0 {
		return nil
	}
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

// DecodeFloat32s unpacks a little-endian byte slice into a float32 slice.
func DecodeFloat32s(b []byte) []float32 {
	if len(b) == 0 || len(b)%4 != 0 {
		return nil
	}
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return out
}

// CosineSimilarity computes cosine similarity between two equal-length vectors.
func CosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		fa := float64(a[i])
		fb := float64(b[i])
		dot += fa * fb
		normA += fa * fa
		normB += fb * fb
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

// OllamaEmbedder calls an OpenAI-compatible /embeddings endpoint (falling back to Ollama /api/embeddings on 404).
type OllamaEmbedder struct {
	baseURL string
	model   string
	client  *http.Client
}

// NewOllamaEmbedder creates an OllamaEmbedder for the given base URL and embedding model.
func NewOllamaEmbedder(baseURL, model string) *OllamaEmbedder {
	return &OllamaEmbedder{
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		client:  &http.Client{Timeout: 5 * time.Second},
	}
}

// Embed returns the embedding vector for text.
func (o *OllamaEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	vec, status, err := o.callOpenAIEmbeddings(ctx, text)
	if err == nil {
		return vec, nil
	}
	if status == http.StatusNotFound {
		return o.callLegacyOllamaEmbeddings(ctx, text)
	}
	return nil, err
}

func (o *OllamaEmbedder) callOpenAIEmbeddings(ctx context.Context, text string) ([]float32, int, error) {
	payload, _ := json.Marshal(map[string]any{
		"model": o.model,
		"input": text,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/embeddings", bytes.NewReader(payload))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.ReadAll(resp.Body)
		return nil, resp.StatusCode, fmt.Errorf("embeddings status %d", resp.StatusCode)
	}

	var out struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, resp.StatusCode, err
	}
	if len(out.Data) == 0 || len(out.Data[0].Embedding) == 0 {
		return nil, resp.StatusCode, fmt.Errorf("empty embedding response")
	}
	return out.Data[0].Embedding, resp.StatusCode, nil
}

func (o *OllamaEmbedder) callLegacyOllamaEmbeddings(ctx context.Context, text string) ([]float32, error) {
	origin := strings.TrimSuffix(o.baseURL, "/v1")
	payload, _ := json.Marshal(map[string]any{
		"model":  o.model,
		"prompt": text,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/api/embeddings", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("legacy ollama embeddings status %d", resp.StatusCode)
	}
	var out struct {
		Embedding []float32 `json:"embedding"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Embedding) == 0 {
		return nil, fmt.Errorf("empty legacy embedding response")
	}
	return out.Embedding, nil
}
