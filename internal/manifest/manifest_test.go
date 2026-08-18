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

// A static header is how a manifest satisfies a provider that requires one:
// Anthropic's /v1/models answers 400 without anthropic-version, and the row
// would report "key check failed" for a good key. It is data in a file, so what
// it may contain is decided at load time and not by a reviewer's attention.
func TestStaticHeadersAreAcceptedButBounded(t *testing.T) {
	withHeaders := func(h string) string {
		return strings.Replace(good, "auth: {type: bearer, env: EXAMPLE_API_KEY}",
			"auth:\n  type: header\n  header: x-api-key\n  env: EXAMPLE_API_KEY\n  headers: {"+h+"}", 1)
	}

	reg, err := load(t, withHeaders(`anthropic-version: "2023-06-01"`))
	if err != nil {
		t.Fatalf("a plain static header must load: %v", err)
	}
	p, _ := reg.Get("example")
	if p.Auth.Headers["anthropic-version"] != "2023-06-01" {
		t.Errorf("the header did not survive loading: %v", p.Auth.Headers)
	}

	refused := map[string]string{
		// The first three would displace something the tool sets itself. The
		// credential one is the reason this check exists at all.
		"the credential header":       `x-api-key: "would-replace-the-key"`,
		"authorization":               `Authorization: "Bearer nope"`,
		"the user-agent aipocket set": `User-Agent: "curl/8"`,
		// The rest are what a name or value may not contain.
		"a newline in the name":            `"x-bad\nInjected: 1": "1"`,
		"a colon in the name":              `"x:bad": "1"`,
		"a non-ascii value":                `x-ok: "café"`,
		"a control character in the value": `x-ok: "a\u0007b"`,
		"an empty value":                   `x-ok: ""`,
	}
	for what, h := range refused {
		_, err := load(t, withHeaders(h))
		if err == nil {
			t.Errorf("%s must be refused", what)
			continue
		}
		// Refused for the right reason. A raw control byte makes yaml.v3 object on
		// its own, which would leave this validation untested while the test still
		// passed.
		if !strings.Contains(err.Error(), "auth.headers") {
			t.Errorf("%s: refused, but not by the header validation: %v", what, err)
		}
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
