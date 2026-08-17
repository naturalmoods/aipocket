package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/naturalmoods/aipocket/internal/manifest"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// A missing config is the normal case: environment variables alone are enough.
func TestMissingConfigIsNotAnError(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil || cfg == nil || cfg.Providers == nil {
		t.Fatalf("got %v, %v", cfg, err)
	}
}

// yaml.v3 quotes the offending scalar in its error, which puts the first
// characters of a mis-pasted API key on stderr and into CI logs. No redactor
// exists yet at config-load time, so the value is stripped instead.
func TestParseErrorDoesNotEchoTheOffendingValue(t *testing.T) {
	p := write(t, "providers:\n  groq: sk-live-0123456789abcdef\n")
	_, err := LoadConfig(p)
	if err == nil {
		t.Fatal("expected a parse error")
	}
	if strings.Contains(err.Error(), "sk-live") {
		t.Fatalf("the value was echoed: %v", err)
	}
}

func TestUnknownFieldIsRejected(t *testing.T) {
	p := write(t, "providers:\n  groq:\n    keyy: env:GROQ_API_KEY\n")
	if _, err := LoadConfig(p); err == nil {
		t.Fatal("a typo in a config field must fail loudly, not silently no-op")
	}
}

// The other half of the same leak, and the one the first fix missed. yaml.v3
// names an unknown *field* in its error, verbatim, so a key pasted where a field
// name belongs reached stderr and CI logs whole — stripping the backquoted part
// of the message only ever covered the value position.
func TestUnknownFieldNameIsNotEchoedEither(t *testing.T) {
	p := write(t, "providers:\n  groq:\n    sk-live-0123456789abcdef: yes\n")
	_, err := LoadConfig(p)
	if err == nil {
		t.Fatal("expected a parse error")
	}
	if strings.Contains(err.Error(), "sk-live") {
		t.Fatalf("the field name was echoed: %v", err)
	}
	// The line number is what a person needs in order to go and look.
	if !strings.Contains(err.Error(), "line 3") {
		t.Errorf("the location is missing: %v", err)
	}
}

// A spec that Parse refuses used to become a per-provider error row, which
// reported one bad line of config as a provider failure.
func TestBadCredentialSpecIsRefusedAtLoadTime(t *testing.T) {
	p := write(t, "providers:\n  groq:\n    key: gsk_A1b2C3d4E5f6G7h8\n")
	_, err := LoadConfig(p)
	if err == nil {
		t.Fatal("a key-shaped credential spec was accepted")
	}
	if strings.Contains(err.Error(), "gsk_") {
		t.Fatalf("the value was echoed: %v", err)
	}
}

// `manual: .nan` is valid YAML. It used to reach the report, where a NaN makes
// the whole JSON document unencodable — one config line blanking out every
// provider's balance.
func TestNonFiniteManualFigureIsRefused(t *testing.T) {
	for _, bad := range []string{".nan", ".inf", "-.inf", "1e400"} {
		p := write(t, "providers:\n  groq:\n    manual: "+bad+"\n")
		if _, err := LoadConfig(p); err == nil {
			t.Errorf("manual: %s was accepted", bad)
		}
	}
	p := write(t, "providers:\n  groq:\n    manual: 25.00\n")
	if _, err := LoadConfig(p); err != nil {
		t.Errorf("an ordinary manual figure was rejected: %v", err)
	}
}

const oneProvider = `
id: groq
name: Groq
status: no-api
console: https://console.groq.com/settings/billing
notes: Groq publishes no balance endpoint.
auth: {type: bearer, env: GROQ_API_KEY}
verify:
  url: https://api.groq.com/openai/v1/models
`

func testRegistry(t *testing.T) *manifest.Registry {
	t.Helper()
	reg, err := manifest.Load(fstest.MapFS{"p/g.yaml": &fstest.MapFile{Data: []byte(oneProvider)}}, "p")
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

// A misspelled id is a typo that reads as a completed action: the config says
// the provider is switched off, and the real provider keeps being contacted with
// the key the user believed they had withdrawn.
func TestUnknownProviderIDInConfigIsRefused(t *testing.T) {
	p := write(t, "providers:\n  groqq:\n    disabled: true\n")
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	err = cfg.ValidateAgainst(testRegistry(t))
	if err == nil {
		t.Fatal("an unknown provider id was accepted")
	}
	if !strings.Contains(err.Error(), "groqq") {
		t.Errorf("the error should name the typo: %v", err)
	}
}

func TestKnownProviderIDsAreAccepted(t *testing.T) {
	p := write(t, "providers:\n  groq:\n    disabled: true\n")
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ValidateAgainst(testRegistry(t)); err != nil {
		t.Fatalf("a known provider was rejected: %v", err)
	}
}

func TestBalanceURLOverrideMustBePlainHTTPS(t *testing.T) {
	for _, bad := range []string{
		"http://api.example.com/v1/credits",
		"https://user:pw@api.example.com/v1/credits",
		"not a url at all",
	} {
		p := write(t, "providers:\n  groq:\n    balance_url: "+bad+"\n")
		if _, err := LoadConfig(p); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	p := write(t, "providers:\n  groq:\n    balance_url: https://api.example.com/v1/credits\n")
	if _, err := LoadConfig(p); err != nil {
		t.Errorf("a valid override was rejected: %v", err)
	}
}
