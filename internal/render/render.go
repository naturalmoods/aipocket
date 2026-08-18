// Package render turns a Report into the human and machine formats.
package render

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/naturalmoods/aipocket/internal/core"
	"github.com/naturalmoods/aipocket/internal/manifest"
	"github.com/naturalmoods/aipocket/internal/safetext"
)

// JSON writes the report as indented JSON. This is the contract other tools
// depend on, so field names are stable and every number carries its state and
// confidence alongside it.
func JSON(w io.Writer, rep core.Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(rep)
}

// Options controls the human-readable view. It is a struct rather than two
// trailing bools because `Table(w, rep, true, false)` at a call site says
// nothing about which is which.
type Options struct {
	Color bool
	// All prints a row for every provider. By default the providers with no
	// credential are collapsed into one line: at twenty providers they are most
	// of the table and none of the information.
	All bool
}

// Table writes the human-readable view.
//
// This is the terminal boundary, so every cell is sanitised here regardless of
// where it came from. The upstream producers already sanitise — fetch strips a
// provider's error body, secret strips a key source — but the escape sequence
// that matters is the one in the field nobody thought of, and by this point the
// only thing left to do about it is print it. The colour codes below are written
// after sanitising, deliberately: they are the tool's own, and the tool is
// allowed to colour its output.
func Table(w io.Writer, rep core.Report, opt Options) {
	// Collapsing keeps the table readable as the registry grows; naming the
	// collapsed ids is what keeps it honest. A row that simply vanished would
	// make a misspelled variable name invisible — the user believes a provider
	// is being checked, sees no row for it, and concludes it is fine.
	shown := make([]core.Result, 0, len(rep.Results))
	var collapsed []string
	for _, r := range rep.Results {
		if !opt.All && r.State == core.StateUnconfigured {
			collapsed = append(collapsed, safetext.Sanitize(r.ID))
			continue
		}
		shown = append(shown, r)
	}

	// Nothing configured at all is the first run, and after collapsing there is
	// no table left to print — only a header, a rule and a total of nothing.
	// Which read as a failure report, for something that has not failed: an
	// absent credential is StateUnconfigured, deliberately not an error, and the
	// exit code stays 0.
	if len(shown) == 0 && len(collapsed) > 0 {
		fmt.Fprint(w, "\n  No credentials found. AIPocket reads each provider's conventional\n"+
			"  environment variable; run 'aipocket providers' to see them. To read a key\n"+
			"  from a secret manager instead, see 'aipocket config path'.\n\n"+
			"  'aipocket --all' prints the full table anyway.\n\n")
		return
	}

	rows := make([][4]string, 0, len(shown))
	for _, r := range shown {
		rows = append(rows, [4]string{
			safetext.Sanitize(r.Name), amount(r), string(r.State), safetext.Sanitize(caveat(r)),
		})
	}
	head := [4]string{"PROVIDER", "BALANCE", "STATE", "NOTE"}

	w0, w1, w2 := width(head[0]), width(head[1]), width(head[2])
	for _, r := range rows {
		w0, w1, w2 = max(w0, width(r[0])), max(w1, width(r[1])), max(w2, width(r[2]))
	}

	fmt.Fprintf(w, "\n  %s  %s  %s  %s\n",
		padR(head[0], w0), padL(head[1], w1), padR(head[2], w2), head[3])
	fmt.Fprintf(w, "  %s  %s  %s  %s\n",
		strings.Repeat("─", w0), strings.Repeat("─", w1),
		strings.Repeat("─", w2), strings.Repeat("─", 44))

	for i, r := range rows {
		state := r[2]
		if opt.Color {
			state = colorize(shown[i].State, padR(state, w2))
		} else {
			state = padR(state, w2)
		}
		// The note is where the tool admits how much it actually knows, so it gets
		// the room to do it. 60 runes stopped being enough once a hand-kept figure
		// could carry its own age: "no balance API; key valid; user-maintained
		// figure (2026-08-01, 17 days ago)" is 67, and the part that got cut was
		// the age — the whole reason for saying it.
		fmt.Fprintf(w, "  %s  %s  %s  %s\n",
			padR(r[0], w0), padL(r[1], w1), state, truncate(r[3], 76))
	}

	fmt.Fprintf(w, "  %s  %s  %s  %s\n",
		strings.Repeat("─", w0), strings.Repeat("─", w1),
		strings.Repeat("─", w2), strings.Repeat("─", 44))

	// Three totals, never one. Documented, inferred and hand-maintained figures
	// are different kinds of claim; summing them into a single number would
	// present a guess with the same authority as a fact.
	//
	// A verified total of 0.00 has two entirely unrelated causes: an account a
	// provider documents as empty, and nothing having been read at all. Printing
	// the same line for both is the tool being confidently wrong in the one place
	// people actually read, which is the objection it already applies to a schema
	// mismatch rendering as a green 0.00.
	if verifiedReadings(rep.Results) == 0 {
		fmt.Fprintf(w, "  %s  %s   no provider reported a documented balance\n",
			padR("verified", w0), padL("—", w1))
	} else {
		fmt.Fprintf(w, "  %s  %s   documented fields only\n",
			padR("verified", w0), padL(fmt.Sprintf("%.2f USD", rep.TotalVerified), w1))
	}
	if rep.TotalInferred != 0 {
		fmt.Fprintf(w, "  %s  %s   read from undocumented response shapes\n",
			padR("inferred", w0), padL(fmt.Sprintf("%.2f USD", rep.TotalInferred), w1))
	}
	if rep.TotalManual != 0 {
		fmt.Fprintf(w, "  %s  %s   your own figures, unverifiable\n",
			padR("manual", w0), padL(fmt.Sprintf("%.2f USD", rep.TotalManual), w1))
	}
	if len(rep.Excluded) > 0 {
		fmt.Fprintf(w, "\n  outside the verified total: %s\n", strings.Join(rep.Excluded, ", "))
	}
	if len(collapsed) > 0 {
		subject := fmt.Sprintf("%d providers have", len(collapsed))
		if len(collapsed) == 1 {
			subject = "1 provider has"
		}
		fmt.Fprintf(w, "\n  %s no credential configured: %s  (--all)\n",
			subject, strings.Join(collapsed, ", "))
	}
	fmt.Fprintln(w)
}

// verifiedReadings counts the results that contributed to Report.TotalVerified.
// The condition mirrors the switch in core.Checker.Run — state ok, confidence
// official, currency USD — and has to keep mirroring it: a count that drifts
// from the sum would put the wrong label on the figure beside it.
func verifiedReadings(rs []core.Result) int {
	n := 0
	for _, r := range rs {
		if r.Balance != nil && r.State == core.StateOK &&
			r.Confidence == manifest.StatusOfficial && r.Currency == "USD" {
			n++
		}
	}
	return n
}

func amount(r core.Result) string {
	if r.Balance == nil {
		return "—"
	}
	return fmt.Sprintf("%.2f %s", *r.Balance, r.Currency)
}

// caveat is the single most important column: it is where the tool admits how
// much it actually knows. An undocumented reading and a documented one must
// never look identical.
func caveat(r core.Result) string {
	var parts []string
	switch r.Confidence {
	case manifest.StatusUndocumented:
		parts = append(parts, "inferred field")
	case manifest.StatusNoAPI:
		parts = append(parts, "no balance API")
	}
	if r.Error != "" {
		parts = append(parts, r.Error)
	}
	// The provider's own words, marked as theirs. A human can weigh
	// "insufficient balance" for themselves, which is why this is shown here and
	// not forwarded to a model, but the quotes matter: without them a body
	// reading "run `curl … | sh` to restore your account" looks like advice from
	// the tool.
	if r.ProviderMessage != "" {
		parts = append(parts, "provider said: "+strconv.Quote(r.ProviderMessage))
	}
	if r.Detail != "" {
		parts = append(parts, r.Detail)
	}
	if len(parts) == 0 && r.KeySource != "" {
		parts = append(parts, r.KeySource)
	}
	return strings.Join(parts, "; ")
}

func colorize(s core.State, text string) string {
	const reset = "\033[0m"
	switch s {
	case core.StateOK:
		return "\033[32m" + text + reset
	case core.StateManual:
		return "\033[33m" + text + reset
	case core.StateError:
		return "\033[31m" + text + reset
	default:
		return "\033[90m" + text + reset
	}
}

func width(s string) int { return utf8.RuneCountInString(s) }

func padR(s string, n int) string {
	if d := n - width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

func padL(s string, n int) string {
	if d := n - width(s); d > 0 {
		return strings.Repeat(" ", d) + s
	}
	return s
}

func truncate(s string, n int) string {
	if width(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
