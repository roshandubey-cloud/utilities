package generator

import (
	"strings"
	"testing"
)

func TestNewMarkerToken_LengthAndAlphabet(t *testing.T) {
	for i := 0; i < 50; i++ {
		tok := NewMarkerToken()
		if len(tok) != MarkerLen {
			t.Fatalf("token length %d, want %d", len(tok), MarkerLen)
		}
		for _, c := range tok {
			ok := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
			if !ok {
				t.Fatalf("token contains non-lowercase-alphanumeric %q in %q", c, tok)
			}
		}
	}
}

func TestNewMarkerToken_Uniqueness(t *testing.T) {
	// 1000 tokens at base 36^12 should never collide. Catches the
	// "fallback to mathrand" branch leaking deterministic seeds.
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		tok := NewMarkerToken()
		if _, dup := seen[tok]; dup {
			t.Fatalf("duplicate token %s after %d draws", tok, i+1)
		}
		seen[tok] = struct{}{}
	}
}

func TestNameFromPatternWithMarker_EmbedsMarkerBeforeExtension(t *testing.T) {
	name := NameFromPatternWithMarker("invoice*.csv", "abc123def456")
	if !strings.HasSuffix(name, ".csv") {
		t.Errorf("extension lost: %s", name)
	}
	if !strings.Contains(name, "_slt_abc123def456_") {
		t.Errorf("marker block missing or mangled: %s", name)
	}
	if !strings.HasPrefix(name, "invoice") {
		t.Errorf("pattern prefix lost: %s", name)
	}
}

func TestNameFromPatternWithMarker_EmptyMarkerFallsBack(t *testing.T) {
	// Empty marker must produce the same shape as NameFromPattern (no
	// _slt_ block) so callers can use one entry point unconditionally.
	name := NameFromPatternWithMarker("invoice*.csv", "")
	if strings.Contains(name, "_slt_") {
		t.Errorf("empty marker should produce no _slt_ block: %s", name)
	}
}

func TestExtractMarker_RoundTripsWithMarker(t *testing.T) {
	tok := NewMarkerToken()
	name := NameFromPatternWithMarker("payroll*.txt", tok)
	got, ok := ExtractMarker(name)
	if !ok {
		t.Fatalf("ExtractMarker missed marker in %s", name)
	}
	if got != tok {
		t.Errorf("got %q, want %q", got, tok)
	}
}

func TestExtractMarker_FindsMarkerWhenServerAddedPrefix(t *testing.T) {
	// The whole point of filename mode: server can prepend bytes and
	// the marker still survives. e.g. an EDI gateway that wraps each
	// file in "EDI_<account>_<orig>".
	tok := "abc123xyz789"
	original := NameFromPatternWithMarker("order*.xml", tok)
	mangled := "EDI_acme_" + original
	got, ok := ExtractMarker(mangled)
	if !ok {
		t.Fatalf("server-prefix variant missed: %s", mangled)
	}
	if got != tok {
		t.Errorf("got %q, want %q", got, tok)
	}
}

func TestExtractMarker_FindsMarkerWhenServerAddedSuffix(t *testing.T) {
	tok := "qwerty12asdf"
	original := NameFromPatternWithMarker("ack*", tok)
	// Server appends a routing tag after the extension.
	mangled := original + "_routed_us-east-1"
	got, ok := ExtractMarker(mangled)
	if !ok {
		t.Fatalf("server-suffix variant missed: %s", mangled)
	}
	if got != tok {
		t.Errorf("got %q, want %q", got, tok)
	}
}

func TestExtractMarker_NoMarker_NoFalsePositive(t *testing.T) {
	for _, name := range []string{
		"invoice123.csv",
		"random_filename_with_underscores.txt",
		"_slt_too_short.csv",         // marker block but token < 12 chars
		"_slt_NOTLOWERnotvalid_.csv", // contains uppercase — alphabet violation
		"",
	} {
		if _, ok := ExtractMarker(name); ok {
			t.Errorf("false positive on %q", name)
		}
	}
}

func TestExtractMarker_CaseInsensitive(t *testing.T) {
	// Some servers normalise to UPPERCASE on storage. We still want a
	// match. The extracted token is lowercased.
	tok := "myMARKERToke"
	original := NameFromPatternWithMarker("file*.csv", strings.ToLower(tok))
	upper := strings.ToUpper(original)
	got, ok := ExtractMarker(upper)
	if !ok {
		t.Fatalf("uppercase server name missed: %s", upper)
	}
	if got != strings.ToLower(tok) {
		t.Errorf("got %q, want lowercased token", got)
	}
}
