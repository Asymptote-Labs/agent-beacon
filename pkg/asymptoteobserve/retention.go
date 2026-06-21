package asymptoteobserve

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// PromptRedactionPlaceholder replaces prompt bodies when prompt retention runs in
// "redacted" mode. It is a fixed marker so downstream readers can recognize that the
// prompt body was intentionally withheld rather than empty.
const PromptRedactionPlaceholder = "[REDACTED]"

// NormalizePromptRetention returns a valid prompt retention mode, defaulting to
// ContentRetentionFull for empty or unrecognized input. The modes reuse the
// content.retention ladder so persisted events and the schema stay aligned.
func NormalizePromptRetention(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case ContentRetentionMetadata:
		return ContentRetentionMetadata
	case ContentRetentionRedacted:
		return ContentRetentionRedacted
	default:
		return ContentRetentionFull
	}
}

// PromptDigest returns a stable sha256 hash (hex) and rune length of raw prompt text.
// It lets operators correlate and dedupe prompts in redacted/metadata modes without
// retaining the prompt body. Empty text yields an empty digest.
func PromptDigest(raw string) (hash string, length int) {
	if raw == "" {
		return "", 0
	}
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:]), len([]rune(raw))
}

// RetainPrompt applies a prompt retention mode to raw prompt text, returning the
// PromptInfo to persist and the content marker that records how the body was handled.
//
//   - full (default, and any unknown mode): the body is kept verbatim and no content
//     marker is added (callers still apply secret redaction and truncation downstream).
//   - redacted: the body is replaced with PromptRedactionPlaceholder and a digest is
//     retained; the marker reports redacted=true.
//   - metadata: the body is dropped entirely and only the digest is retained.
//
// RetainPrompt never returns a nil PromptInfo so callers can persist the digest.
func RetainPrompt(mode, raw string) (*PromptInfo, *ContentInfo) {
	switch NormalizePromptRetention(mode) {
	case ContentRetentionRedacted:
		hash, length := PromptDigest(raw)
		return &PromptInfo{Text: PromptRedactionPlaceholder, Hash: hash, Length: length},
			&ContentInfo{Retention: ContentRetentionRedacted, Included: true, Redacted: true}
	case ContentRetentionMetadata:
		hash, length := PromptDigest(raw)
		return &PromptInfo{Hash: hash, Length: length},
			&ContentInfo{Retention: ContentRetentionMetadata, Included: false}
	default:
		return &PromptInfo{Text: raw}, nil
	}
}
