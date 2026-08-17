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

// Table writes the human-readable view.
//
// This is the terminal boundary, so every cell is sanitised here regardless of
// where it came from. The upstream producers already sanitise — fetch strips a
// provider's error body, secret strips a key source — but the escape sequence
// that matters is the one in the field nobody thought of, and by this point the
// only thing left to do about it is print it. The colour codes below are written
// after sanitising, deliberately: they are the tool's own, and the tool is
// allowed to colour its output.
func Table(w io.Writer, rep core.Report, color bool) {
	rows := make([][4]string, 0, len(rep.Results))
	for _, r := range rep.Results {
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
		if color {
			state = colorize(rep.Results[i].State, padR(state, w2))
		} else {
			state = padR(state, w2)
		}
		fmt.Fprintf(w, "  %s  %s  %s  %s\n",
			padR(r[0], w0), padL(r[1], w1), state, truncate(r[3], 60))
	}

	fmt.Fprintf(w, "  %s  %s  %s  %s\n",
		strings.Repeat("─", w0), strings.Repeat("─", w1),
		strings.Repeat("─", w2), strings.Repeat("─", 44))

	// Three totals, never one. Documented, inferred and hand-maintained figures
	// are different kinds of claim; summing them into a single number would
	// present a guess with the same authority as a fact.
	fmt.Fprintf(w, "  %s  %s   documented fields only\n",
		padR("verified", w0), padL(fmt.Sprintf("%.2f USD", rep.TotalVerified), w1))
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
	fmt.Fprintln(w)
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
