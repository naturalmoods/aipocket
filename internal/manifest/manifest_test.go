package manifest

import (
	"encoding/json"
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

// Some providers report money in fixed-point units — novita in 1/10000 USD — and
// a manifest that could not convert them would report a balance ten thousand times
// too large. money.Plausible is no defence there: a million dollars is a plausible
// amount of money.
func TestScaleConvertsTheProvidersUnitAndIsBounded(t *testing.T) {
	withAmount := func(amount string) string {
		return strings.Replace(good,
			"- {total: $.total, used: $.used, currency: USD}", amount, 1)
	}
	resolve := func(t *testing.T, y, doc string) (float64, bool) {
		t.Helper()
		reg, err := load(t, y)
		if err != nil {
			t.Fatalf("manifest was refused: %v", err)
		}
		p, _ := reg.Get("example")
		var v any
		if err := json.Unmarshal([]byte(doc), &v); err != nil {
			t.Fatal(err)
		}
		return p.Balance.Amounts[0].Resolve(v)
	}

	got, ok := resolve(t, withAmount(`- {path: $.b, scale: 0.0001, currency: USD}`), `{"b":1000000}`)
	if !ok || got != 100 {
		t.Errorf("scaled reading = %v (ok=%v), want 100", got, ok)
	}

	// The same arithmetic has to reach a total-minus-used pair, since a provider
	// reporting lifetime figures in fixed-point units reports both that way.
	got, ok = resolve(t, withAmount(`- {total: $.t, used: $.u, scale: 0.01, currency: USD}`), `{"t":5000,"u":1500}`)
	if !ok || got != 35 {
		t.Errorf("scaled difference = %v (ok=%v), want 35", got, ok)
	}

	// The check runs on the scaled figure, because that is the one that would be
	// reported. 1e11 units at 1000 each is not money.
	if _, ok := resolve(t, withAmount(`- {path: $.b, scale: 1000, currency: USD}`), `{"b":100000000000}`); ok {
		t.Error("a scaled figure outside the plausible range must not resolve")
	}

	refused := map[string]string{
		// Zero is the dangerous one: it would report every account as empty, and
		// a pointer field is what makes it distinguishable from no scale at all.
		"zero":     `- {path: $.b, scale: 0, currency: USD}`,
		"negative": `- {path: $.b, scale: -0.0001, currency: USD}`,
		"nan":      `- {path: $.b, scale: .nan, currency: USD}`,
		"infinite": `- {path: $.b, scale: .inf, currency: USD}`,
	}
	for what, amount := range refused {
		if _, err := load(t, withAmount(amount)); err == nil {
			t.Errorf("scale %s must be refused", what)
		}
	}
}

// The scheme is written directly in front of a credential, so what may stand
// there is decided at load time and not by a reviewer noticing.
func TestAuthSchemeIsBoundedAndOnlyMeansSomethingForBearer(t *testing.T) {
	ok := strings.Replace(good, "auth: {type: bearer, env: EXAMPLE_API_KEY}",
		"auth: {type: bearer, scheme: Key, env: EXAMPLE_API_KEY}", 1)
	reg, err := load(t, ok)
	if err != nil {
		t.Fatalf("a plain scheme must load: %v", err)
	}
	if p, _ := reg.Get("example"); p.Auth.AuthScheme() != "Key" {
		t.Errorf("scheme = %q", p.Auth.AuthScheme())
	}
	// The default matters more than the field: every existing manifest relies on
	// it.
	if reg, err := load(t, good); err != nil {
		t.Fatal(err)
	} else if p, _ := reg.Get("example"); p.Auth.AuthScheme() != "Bearer" {
		t.Errorf("a manifest without a scheme must send Bearer, got %q", p.Auth.AuthScheme())
	}

	refused := map[string]string{
		"two words":            `auth: {type: bearer, scheme: "Key Bearer", env: EXAMPLE_API_KEY}`,
		"a newline":            `auth: {type: bearer, scheme: "Key\nX-Evil: 1", env: EXAMPLE_API_KEY}`,
		"a colon":              `auth: {type: bearer, scheme: "Key:", env: EXAMPLE_API_KEY}`,
		"leading digit":        `auth: {type: bearer, scheme: "1Key", env: EXAMPLE_API_KEY}`,
		"together with header": `auth: {type: header, header: x-api-key, scheme: Key, env: EXAMPLE_API_KEY}`,
	}
	for what, line := range refused {
		y := strings.Replace(good, "auth: {type: bearer, env: EXAMPLE_API_KEY}", line, 1)
		err := func() error { _, err := load(t, y); return err }()
		if err == nil {
			t.Errorf("%s must be refused", what)
			continue
		}
		if !strings.Contains(err.Error(), "auth.scheme") {
			t.Errorf("%s: refused, but not by the scheme validation: %v", what, err)
		}
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
