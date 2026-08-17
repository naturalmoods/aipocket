// Package safetext makes untrusted text safe to put in front of a human.
//
// The untrusted text in question is a provider's HTTP error body. It is
// genuinely useful ("insufficient balance", "key revoked") which is why it is
// shown at all, but it is attacker- or accident-controlled bytes on their way
// to a terminal:
//
//   - an ANSI escape can repaint the screen, so a 402 body can overwrite the
//     "verified" total that was printed above it, or hide itself entirely;
//   - an OSC 8 / OSC 52 sequence can plant a hyperlink or push data into the
//     clipboard in several common terminals;
//   - a bidi override (U+202E) can display a reversed string, the Trojan Source
//     trick, so what a user reads is not what the tool received;
//   - a raw newline breaks the table apart and lets a body forge extra rows.
//
// None of that is exotic to defend against: money figures and error prose need
// no control characters at all, so every one of them is dropped.
package safetext

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Sanitize strips control, format, surrogate and private-use characters. Tab,
// newline, carriage return, vertical tab and form feed become a single space so
// a multi-line body cannot forge table rows; invalid UTF-8 becomes U+FFFD.
//
// Ordering note for callers: redaction runs *before* this, never after. A
// redactor matches whole values, and rewriting the text first could split a
// secret across the substitution and leave half of it behind.
func Sanitize(s string) string {
	// The common case is text that needs no change at all; scan first so the
	// happy path does not allocate.
	if !needsSanitizing(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i, r := range s {
		switch {
		case r == utf8.RuneError:
			// Either invalid UTF-8 (width 1) or a genuine U+FFFD in the input;
			// both render as the replacement character.
			_, w := utf8.DecodeRuneInString(s[i:])
			if w == 1 {
				b.WriteRune(utf8.RuneError)
				continue
			}
			b.WriteRune(r)
		case r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f':
			b.WriteByte(' ')
		case unicode.Is(unicode.Cc, r), unicode.Is(unicode.Cf, r),
			unicode.Is(unicode.Cs, r), unicode.Is(unicode.Co, r):
			// Dropped, not replaced: leaving a space where an ESC was would
			// still let "[31m" pass as decoration, and the goal is inert text.
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func needsSanitizing(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r == utf8.RuneError || r > unicode.MaxASCII &&
			(unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r) ||
				unicode.Is(unicode.Cs, r) || unicode.Is(unicode.Co, r)) {
			return true
		}
	}
	return false
}
