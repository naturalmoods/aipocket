package manifest

import (
	"strings"
	"testing"
	"testing/fstest"
)

func load(t *testing.T, yaml string) (*Registry, error) {
	t.Helper()
	return Load(fstest.MapFS{"p/x.yaml": &fstest.MapFile{Data: []byte(yaml)}}, "p")
}

const good = `
id: example
name: Example
status: official
console: https://example.com/billing
docs: https://example.com/docs
auth: {type: bearer, env: EXAMPLE_API_KEY}
balance:
  url: https://api.example.com/v1/credits
  amounts:
    - {total: $.total, used: $.used, currency: USD}
`

func TestLoadGood(t *testing.T) {
	reg, err := load(t, good)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := reg.Get("example")
	if !ok {
		t.Fatal("provider not found")
	}
	if p.Balance.Host() != "api.example.com" {
		t.Errorf("host = %q", p.Balance.Host())
	}
}

// The URL in a manifest decides where an API key is sent. These rules are
// mechanical on purpose: they must not depend on a reviewer noticing.
func TestRejectsUnsafeURLs(t *testing.T) {
	cases := map[string]string{
		"plaintext http":  strings.Replace(good, "https://api.example.com", "http://api.example.com", 1),
		"userinfo in url": strings.Replace(good, "https://api.example.com", "https://user:pw@api.example.com", 1),
		"no host":         strings.Replace(good, "https://api.example.com/v1/credits", "https:///v1/credits", 1),
	}
	for name, y := range cases {
		if _, err := load(t, y); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
}

func TestRejectsInconsistentStatus(t *testing.T) {
	cases := map[string]string{
		"no-api with balance": strings.Replace(good, "status: official", "status: no-api", 1),
		"unknown status":      strings.Replace(good, "status: official", "status: probably-fine", 1),
	}
	for name, y := range cases {
		if _, err := load(t, y); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
}

// A typo in a manifest field must fail loudly. Silently ignoring an unknown
// key is how a provider ends up reporting nothing while looking configured.
func TestRejectsUnknownFields(t *testing.T) {
	y := good + "\nballance: oops\n"
	if _, err := load(t, y); err == nil {
		t.Fatal("expected unknown field to be rejected")
	}
}

func TestRejectsAmbiguousAmount(t *testing.T) {
	y := strings.Replace(good,
		"- {total: $.total, used: $.used, currency: USD}",
		"- {path: $.a, total: $.total, used: $.used, currency: USD}", 1)
	if _, err := load(t, y); err == nil {
		t.Fatal("expected path+total/used to be rejected")
	}
}

func TestRejectsAmountWithoutCurrency(t *testing.T) {
	y := strings.Replace(good,
		"- {total: $.total, used: $.used, currency: USD}",
		"- {total: $.total, used: $.used}", 1)
	if _, err := load(t, y); err == nil {
		t.Fatal("expected missing currency to be rejected")
	}
}

func TestRejectsBadJPath(t *testing.T) {
	y := strings.Replace(good, "$.total", "total", 1)
	if _, err := load(t, y); err == nil {
		t.Fatal("expected invalid jpath to be rejected at load time")
	}
}

func TestDuplicateIDs(t *testing.T) {
	fs := fstest.MapFS{
		"p/a.yaml": &fstest.MapFile{Data: []byte(good)},
		"p/b.yaml": &fstest.MapFile{Data: []byte(good)},
	}
	if _, err := Load(fs, "p"); err == nil {
		t.Fatal("expected duplicate id to be rejected")
	}
}
