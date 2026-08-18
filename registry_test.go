package aipocket_test

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/naturalmoods/aipocket"
	"github.com/naturalmoods/aipocket/internal/manifest"
)

// The registry is embedded, so a malformed manifest must fail the build's test
// run rather than a user's morning. This is the gate that makes "adding a
// provider is just a YAML file" safe to promise to contributors.
func TestEmbeddedRegistryIsValid(t *testing.T) {
	reg, err := aipocket.Registry()
	if err != nil {
		t.Fatalf("embedded registry does not load: %v", err)
	}
	if len(reg.IDs()) == 0 {
		t.Fatal("registry is empty")
	}
}

func TestEveryProviderIsHonestAboutItself(t *testing.T) {
	reg, err := aipocket.Registry()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range reg.All() {
		t.Run(p.ID, func(t *testing.T) {
			if p.Console == "" {
				t.Error("console url is required: a user must be able to check by hand")
			}
			// An inferred reading without a note is the failure mode this
			// project exists to avoid — the number looks as solid as any other.
			if p.Status == manifest.StatusUndocumented && p.Notes == "" {
				t.Error("status 'undocumented' requires notes explaining what was inferred")
			}
			if p.Status == manifest.StatusNoAPI {
				if p.Balance != nil {
					t.Error("status 'no-api' must not define a balance endpoint")
				}
				if p.Notes == "" {
					t.Error("status 'no-api' requires notes saying so")
				}
			}
			if p.Status == manifest.StatusOfficial && p.Docs == "" {
				t.Error("status 'official' requires a docs link to back the claim")
			}
			for _, ep := range []*manifest.Endpoint{p.Balance, p.Verify} {
				if ep == nil {
					continue
				}
				if !strings.HasPrefix(ep.URL, "https://") {
					t.Errorf("%s is not https", ep.URL)
				}
			}
		})
	}
}

// checkedOn is the form a `no-api` note has to carry.
var checkedOn = regexp.MustCompile(`checked \d{4}-\d{2}-\d{2}`)

// `status: no-api` is the only claim in the registry that no test can verify:
// it says something about a provider's documentation rather than about this
// code. It is also the only one that rots on its own — a provider ships a
// credits API, this file keeps saying there is none, and nothing anywhere
// notices. The date does not make the claim true; it makes it auditable. A
// reader can see when it was last looked at and decide whether that is recent
// enough to trust, which is the same reason `undocumented` has to say what was
// guessed.
func TestNoAPIClaimsCarryTheDateTheyWereChecked(t *testing.T) {
	reg, err := aipocket.Registry()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range reg.All() {
		if p.Status != manifest.StatusNoAPI {
			continue
		}
		if !checkedOn.MatchString(p.Notes) {
			t.Errorf("%s: notes must record when the absence of a balance API was "+
				"last confirmed, in the form `checked YYYY-MM-DD`", p.ID)
		}
	}
}

// Every provider that reports a balance, resolved through the manifest that
// ships against the example response its own documentation publishes.
//
// A typo in a jpath is the likeliest mistake in one of these files, and without
// this test it surfaces as "the provider answered but no balance field matched"
// against a live account — at the exact moment someone needed the number. What
// this does not claim is that the provider still returns that shape; the visible
// error in core is for that. It claims the manifest agrees with the provider's
// own documentation, which is the part a test can settle.
func TestBalancePathsResolveTheProvidersDocumentedExample(t *testing.T) {
	cases := map[string]struct {
		body     string
		want     float64
		currency string
	}{
		"moonshot": {
			body: `{"code":0,"data":{"available_balance":49.58894,
			        "voucher_balance":46.58893,"cash_balance":3.00001},
			        "scode":"0x0","status":true}`,
			want: 49.58894, currency: "USD",
		},
		"siliconflow": {
			// The figures are JSON strings in the documented example, which is
			// exactly the kind of detail a manifest gets wrong silently.
			body: `{"code":20000,"message":"OK","status":true,"data":{"id":"userid",
			        "name":"username","isAdmin":false,"balance":"0.88",
			        "status":"normal","chargeBalance":"88.00","totalBalance":"88.88"}}`,
			want: 88.88, currency: "USD",
		},
		"fal": {
			body: `{"username":"my-team","credits":{"current_balance":24.5,
			        "currency":"USD"}}`,
			want: 24.5, currency: "USD",
		},
	}

	reg, err := aipocket.Registry()
	if err != nil {
		t.Fatal(err)
	}
	for id, c := range cases {
		t.Run(id, func(t *testing.T) {
			p, ok := reg.Get(id)
			if !ok {
				t.Fatalf("provider %q is not in the registry", id)
			}
			if p.Balance == nil {
				t.Fatalf("%s reports no balance", id)
			}
			var doc any
			if err := json.Unmarshal([]byte(c.body), &doc); err != nil {
				t.Fatalf("the example response in this test is not valid JSON: %v", err)
			}
			for _, a := range p.Balance.Amounts {
				v, ok := a.Resolve(doc)
				if !ok {
					continue
				}
				if v != c.want {
					t.Errorf("read %v from the documented example, want %v", v, c.want)
				}
				if a.Currency != c.currency {
					t.Errorf("currency = %s, want %s", a.Currency, c.currency)
				}
				return
			}
			t.Error("no amount in the manifest matched the provider's own documented example")
		})
	}
}

func TestKnownProvidersPresent(t *testing.T) {
	reg, err := aipocket.Registry()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{
		"openrouter", "deepseek", "groq", "neuralwatt", "entrim",
		"openai", "anthropic", "gemini", "mistral", "xai", "together", "cerebras",
		"moonshot", "siliconflow", "replicate", "deepinfra", "nebius", "fal",
	} {
		if _, ok := reg.Get(id); !ok {
			t.Errorf("provider %q missing from registry", id)
		}
	}
}
