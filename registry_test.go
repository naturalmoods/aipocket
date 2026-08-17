package aipocket_test

import (
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

func TestKnownProvidersPresent(t *testing.T) {
	reg, err := aipocket.Registry()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"openrouter", "deepseek", "groq", "neuralwatt", "entrim"} {
		if _, ok := reg.Get(id); !ok {
			t.Errorf("provider %q missing from registry", id)
		}
	}
}
