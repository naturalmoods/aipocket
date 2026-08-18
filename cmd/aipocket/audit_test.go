package main

import (
	"io"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/naturalmoods/aipocket/internal/core"
	"github.com/naturalmoods/aipocket/internal/manifest"
)

// `aipocket audit` promises the complete network footprint before anyone trusts
// the tool with a credential. A static header is part of what gets sent, so
// omitting it would make that promise false in a small, quiet way — the kind of
// gap that is only ever found by the person who did not need to find it.
func TestAuditNamesTheStaticHeadersItWouldSend(t *testing.T) {
	y := `
id: acme
name: Acme
status: no-api
console: https://acme.test/billing
notes: "checked 2026-08-18: no balance endpoint is documented."
auth:
  type: header
  header: x-api-key
  env: ACME_API_KEY
  headers: {acme-version: "2026-01-01"}
verify:
  url: https://api.acme.test/v1/models
`
	reg, err := manifest.Load(fstest.MapFS{"p/acme.yaml": &fstest.MapFile{Data: []byte(y)}}, "p")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &core.Config{Providers: map[string]core.ProviderConfig{}}

	if out := captureStdout(t, func() { audit(reg, cfg, false) }); !strings.Contains(out, "acme-version: 2026-01-01") {
		t.Errorf("audit does not name the static header it would send:\n%s", out)
	}
	if out := captureStdout(t, func() { audit(reg, cfg, true) }); !strings.Contains(out, `"acme-version": "2026-01-01"`) {
		t.Errorf("the machine-readable footprint omits the header:\n%s", out)
	}
}

// A provider with no static headers must not grow an empty line or an empty JSON
// object; audit is read by people, and noise there costs attention that the
// hosts and key sources need.
func TestAuditSaysNothingAboutHeadersWhenThereAreNone(t *testing.T) {
	y := `
id: acme
name: Acme
status: official
console: https://acme.test/billing
docs: https://acme.test/docs
auth: {type: bearer, env: ACME_API_KEY}
balance:
  url: https://api.acme.test/v1/credits
  amounts: [{path: $.balance, currency: USD}]
`
	reg, err := manifest.Load(fstest.MapFS{"p/acme.yaml": &fstest.MapFile{Data: []byte(y)}}, "p")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &core.Config{Providers: map[string]core.ProviderConfig{}}

	if out := captureStdout(t, func() { audit(reg, cfg, false) }); strings.Contains(out, "also sends") {
		t.Errorf("audit invented a headers line:\n%s", out)
	}
	if out := captureStdout(t, func() { audit(reg, cfg, true) }); strings.Contains(out, "headers") {
		t.Errorf("the JSON footprint carries an empty headers field:\n%s", out)
	}
}

// Every probe path is one authenticated request the user can trigger, so audit
// has to account for all of them — and a duplicate would be a wasted request sent
// twice, while a path missing its leading slash would quietly point somewhere else
// entirely.
func TestEveryProbePathIsListedOnceAndRooted(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range probePaths {
		if !strings.HasPrefix(p, "/") {
			t.Errorf("probe path %q is not rooted", p)
		}
		if seen[p] {
			t.Errorf("probe path %q is listed twice: the key would be sent to it twice", p)
		}
		seen[p] = true
	}

	y := `
id: acme
name: Acme
status: official
console: https://acme.test/billing
docs: https://acme.test/docs
auth: {type: bearer, env: ACME_API_KEY}
balance:
  url: https://api.acme.test/v1/credits
  amounts: [{path: $.balance, currency: USD}]
`
	reg, err := manifest.Load(fstest.MapFS{"p/acme.yaml": &fstest.MapFile{Data: []byte(y)}}, "p")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &core.Config{Providers: map[string]core.ProviderConfig{}}

	out := captureStdout(t, func() { audit(reg, cfg, false) })
	for _, p := range probePaths {
		if !strings.Contains(out, "https://api.acme.test"+p) {
			t.Errorf("audit does not name the probe request for %q — the footprint is\n"+
				"incomplete, which is the one promise audit makes:\n%s", p, out)
		}
	}
}

// captureStdout collects what f prints. audit writes to os.Stdout directly,
// which is right for a command and means a test has to swap it.
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	captured := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		captured <- string(b)
	}()

	old := os.Stdout
	os.Stdout = w
	f()
	os.Stdout = old
	w.Close()
	return <-captured
}
