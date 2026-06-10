package indexer

import (
	"context"
	"errors"
	"strings"
)

// isTransientHTTPStatusText matches compact errors like 'status 503: …' from native HTTP
// embedders (Ollama, proxies). OpenAI-style bodies instead contain phrases such as
// 'status code: 503', covered by recoverableEmbedErrorSubstrings).
func isTransientHTTPStatusText(msg string) bool {
	msg = strings.ToLower(msg)
	for _, code := range []string{"status 429", "status 500", "status 502", "status 503", "status 504"} {
		if strings.Contains(msg, code) {
			return true
		}
	}
	return false
}

// Recoverable embed error substrings (external API / transient). When the embedder returns
// one of these, the indexer skips storing chunks for that file and continues with others.
var recoverableEmbedErrorSubstrings = []string{
	"status code: 500", "500 Internal Server Error", "Internal Server Error",
	"status code: 502", "502 Bad Gateway", "Bad Gateway",
	"status code: 503", "503 Service Unavailable", "Service Unavailable",
	"status code: 429", "429 Too Many Requests", "rate limit",
	"timeout", "deadline exceeded", "connection refused", "connection reset",
	"temporary failure", "try again later", "overloaded",
}

// IsEmbeddingInputLimitError is true when the embedding API rejected an input for exceeding
// the model's token/length limit (e.g. OpenAI "maximum input length is 8192 tokens").
// The indexer skips vectors for that file's batch and continues; metadata/chunks without embed may still apply depending on caller.
func IsEmbeddingInputLimitError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "maximum input length") {
		return true
	}
	if strings.Contains(msg, "maximum context length") {
		return true
	}
	if strings.Contains(msg, "context length") && strings.Contains(msg, "exceed") {
		return true
	}
	if strings.Contains(msg, "string_above_max_length") {
		return true
	}
	if strings.Contains(msg, "too long") && strings.Contains(msg, "token") {
		return true
	}
	return false
}

// IsEmbeddingProviderLimitError is true when embedding provider rejected the request/response size.
// Includes per-input token limits and provider payload-size limits (e.g. HTTP 413).
func IsEmbeddingProviderLimitError(err error) bool {
	if err == nil {
		return false
	}
	if IsEmbeddingInputLimitError(err) {
		return true
	}
	msg := strings.ToLower(err.Error())
	limitHints := []string{
		"status code: 413",
		"413 payload too large",
		"payload too large",
		"request too large",
		"request body too large",
		"maximum request size",
		"response too large",
		"maximum response size",
		"too many inputs",
		"array too long",
		"input is too long",
	}
	for _, hint := range limitHints {
		if strings.Contains(msg, hint) {
			return true
		}
	}
	return false
}

// IsRecoverableEmbedError returns true if err is a transient or external API failure
// (e.g. OpenAI 500, rate limit, timeout) or a per-input limit the indexer should not treat as fatal
// (embedding input exceeds model max — skip that file's embeddings and continue). When true, the indexer should log,
// skip embedding for that file/batch, and continue indexing other files.
func IsRecoverableEmbedError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if IsEmbeddingProviderLimitError(err) {
		return true
	}
	msg := strings.ToLower(err.Error())
	if isTransientHTTPStatusText(msg) {
		return true
	}
	for _, sub := range recoverableEmbedErrorSubstrings {
		if strings.Contains(msg, strings.ToLower(sub)) {
			return true
		}
	}
	return false
}
