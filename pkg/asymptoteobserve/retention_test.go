package asymptoteobserve

import "testing"

func TestNormalizePromptRetention(t *testing.T) {
	cases := map[string]string{
		"":          ContentRetentionFull,
		"full":      ContentRetentionFull,
		"FULL":      ContentRetentionFull,
		" redacted": ContentRetentionRedacted,
		"metadata":  ContentRetentionMetadata,
		"bogus":     ContentRetentionFull,
	}
	for input, want := range cases {
		if got := NormalizePromptRetention(input); got != want {
			t.Fatalf("NormalizePromptRetention(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRetainPromptFullKeepsBodyAndAddsNoMarker(t *testing.T) {
	info, content := RetainPrompt("full", "review token=secret")
	if info == nil || info.Text != "review token=secret" {
		t.Fatalf("full mode info = %#v, want verbatim text", info)
	}
	if info.Hash != "" || info.Length != 0 {
		t.Fatalf("full mode should not add a digest: %#v", info)
	}
	if content != nil {
		t.Fatalf("full mode should not add a content marker: %#v", content)
	}
}

func TestRetainPromptRedactedReplacesBodyWithDigest(t *testing.T) {
	raw := "deploy with api_key=hunter2"
	info, content := RetainPrompt("redacted", raw)
	if info.Text != PromptRedactionPlaceholder {
		t.Fatalf("redacted mode text = %q, want placeholder", info.Text)
	}
	wantHash, wantLen := PromptDigest(raw)
	if info.Hash != wantHash || info.Length != wantLen {
		t.Fatalf("redacted digest = (%q,%d), want (%q,%d)", info.Hash, info.Length, wantHash, wantLen)
	}
	if content == nil || content.Retention != ContentRetentionRedacted || !content.Included || !content.Redacted {
		t.Fatalf("redacted marker = %#v", content)
	}
}

func TestRetainPromptMetadataDropsBody(t *testing.T) {
	raw := "summarize this file"
	info, content := RetainPrompt("metadata", raw)
	if info.Text != "" {
		t.Fatalf("metadata mode should drop body, got %q", info.Text)
	}
	wantHash, wantLen := PromptDigest(raw)
	if info.Hash != wantHash || info.Length != wantLen {
		t.Fatalf("metadata digest = (%q,%d), want (%q,%d)", info.Hash, info.Length, wantHash, wantLen)
	}
	if content == nil || content.Retention != ContentRetentionMetadata || content.Included {
		t.Fatalf("metadata marker = %#v", content)
	}
}

func TestPromptDigestStableAndEmpty(t *testing.T) {
	if h, l := PromptDigest(""); h != "" || l != 0 {
		t.Fatalf("empty digest = (%q,%d), want empty", h, l)
	}
	h1, l1 := PromptDigest("café")
	h2, l2 := PromptDigest("café")
	if h1 != h2 || l1 != l2 {
		t.Fatalf("digest not stable: (%q,%d) vs (%q,%d)", h1, l1, h2, l2)
	}
	if l1 != 4 {
		t.Fatalf("rune length = %d, want 4", l1)
	}
}
