package safetext

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// A provider error body reaches a terminal. An ANSI escape in it could repaint
// the screen — including the verified total printed above it — so the escape
// must not survive as an escape.
func TestANSIEscapesCannotReachTheTerminal(t *testing.T) {
	for _, in := range []string{
		"\x1b[2J\x1b[Hverified  999.00 USD",
		"\x1b]8;;https://evil.test\x07click me\x1b]8;;\x07",
		"\x1b]52;c;cGF5bG9hZA==\x07", // OSC 52: clipboard write
		"bell\x07",
	} {
		got := Sanitize(in)
		if strings.ContainsAny(got, "\x1b\x07") {
			t.Errorf("Sanitize(%q) left an escape: %q", in, got)
		}
	}
}

// U+202E displays the rest of the line right-to-left, so a body can show a
// number that is not the one the tool read.
func TestBidiOverridesAreRemoved(t *testing.T) {
	got := Sanitize("balance ‮00.99‬ USD")
	for _, bad := range []rune{'‮', '‬'} {
		if strings.ContainsRune(got, bad) {
			t.Errorf("Sanitize left %U: %q", bad, got)
		}
	}
}

// A raw newline in a cell would break the table apart and let a provider body
// forge extra rows under the totals.
func TestNewlinesBecomeSpacesSoATableCannotBeForged(t *testing.T) {
	got := Sanitize("insufficient balance\n  fake-provider  999.00 USD  ok")
	if strings.ContainsAny(got, "\n\r\t\v\f") {
		t.Fatalf("whitespace control survived: %q", got)
	}
	if !strings.Contains(got, "insufficient balance") {
		t.Fatalf("readable text was mangled: %q", got)
	}
}

func TestOrdinaryTextIsUnchanged(t *testing.T) {
	for _, in := range []string{
		"", "insufficient balance", "37.65 USD", "余额不足", "solde insuffisant",
	} {
		if got := Sanitize(in); got != in {
			t.Errorf("Sanitize(%q) = %q; ordinary text must survive intact", in, got)
		}
	}
}

func TestInvalidUTF8BecomesTheReplacementCharacter(t *testing.T) {
	got := Sanitize("ok\xff\xfeok")
	// Byte-wise, not ContainsAny: the needle would itself decode to U+FFFD.
	if strings.IndexByte(got, 0xff) >= 0 || strings.IndexByte(got, 0xfe) >= 0 {
		t.Fatalf("invalid bytes survived: %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("output is not valid UTF-8: %q", got)
	}
	if !strings.HasPrefix(got, "ok") || !strings.HasSuffix(got, "ok") {
		t.Fatalf("surrounding text lost: %q", got)
	}
}
