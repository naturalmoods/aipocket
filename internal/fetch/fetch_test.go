package fetch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/naturalmoods/aipocket/internal/manifest"
	"testing/fstest"
)

func endpoint(t *testing.T, url string) (*manifest.Endpoint, manifest.Auth) {
	t.Helper()
	y := fmt.Sprintf(`
id: t
name: T
status: official
console: https://t.test/c
docs: https://t.test/d
auth: {type: bearer, env: T_KEY}
balance:
  url: %s
  amounts: [{path: $.balance, currency: USD}]
`, url)
	reg, err := manifest.Load(fstest.MapFS{"p/t.yaml": &fstest.MapFile{Data: []byte(y)}}, "p")
	if err != nil {
		t.Fatal(err)
	}
	p, _ := reg.Get("t")
	return p.Balance, p.Auth
}

// A provider that requires a static header gets it, and the credential still
// travels in its own.
func TestStaticHeadersTravelWithTheRequest(t *testing.T) {
	var sawStatic, sawCredential string
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawStatic = r.Header.Get("acme-version")
		sawCredential = r.Header.Get("Authorization")
		fmt.Fprint(w, `{"balance":1}`)
	}))
	defer ts.Close()

	ep, auth := endpoint(t, ts.URL)
	auth.Headers = map[string]string{"acme-version": "2026-01-01"}

	c := NewWithTransport(ts.Client().Transport, 5*time.Second)
	if _, err := c.Get(context.Background(), ep, auth, "sk-test-key-value"); err != nil {
		t.Fatal(err)
	}
	if sawStatic != "2026-01-01" {
		t.Errorf("static header not sent: %q", sawStatic)
	}
	if sawCredential != "Bearer sk-test-key-value" {
		t.Errorf("credential header = %q", sawCredential)
	}
}

// Not every provider spells the scheme "Bearer": fal documents
// `Authorization: Key <key>`, and without a scheme the tool cannot read its
// balance at all.
func TestTheAuthorizationSchemeCanBeSetAndDefaultsToBearer(t *testing.T) {
	var seen string
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		fmt.Fprint(w, `{"balance":1}`)
	}))
	defer ts.Close()

	ep, auth := endpoint(t, ts.URL)
	c := NewWithTransport(ts.Client().Transport, 5*time.Second)

	auth.Scheme = "Key"
	if _, err := c.Get(context.Background(), ep, auth, "sk-test-key-value"); err != nil {
		t.Fatal(err)
	}
	if seen != "Key sk-test-key-value" {
		t.Errorf("Authorization = %q, want the manifest's scheme", seen)
	}

	// The default is the compatibility half: sixteen manifests say nothing about
	// a scheme and must keep sending Bearer.
	auth.Scheme = ""
	if _, err := c.Get(context.Background(), ep, auth, "sk-test-key-value"); err != nil {
		t.Fatal(err)
	}
	if seen != "Bearer sk-test-key-value" {
		t.Errorf("Authorization = %q, want Bearer by default", seen)
	}
}

// Defence in depth. Manifest validation refuses a static header that would
// displace the credential, so reaching this state means that check was bypassed
// or broken — which is exactly when the ordering inside Get has to hold. The key
// must still be the thing sent, never a literal from a data file.
func TestAStaticHeaderCannotDisplaceTheCredential(t *testing.T) {
	var sawKey string
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawKey = r.Header.Get("x-api-key")
		fmt.Fprint(w, `{"balance":1}`)
	}))
	defer ts.Close()

	ep, _ := endpoint(t, ts.URL)
	auth := manifest.Auth{
		Type: "header", Header: "x-api-key", Env: "T_KEY",
		Headers: map[string]string{"x-api-key": "a-literal-from-a-manifest"},
	}

	c := NewWithTransport(ts.Client().Transport, 5*time.Second)
	if _, err := c.Get(context.Background(), ep, auth, "sk-test-key-value"); err != nil {
		t.Fatal(err)
	}
	if sawKey != "sk-test-key-value" {
		t.Errorf("the credential header carried %q instead of the key", sawKey)
	}
}

// A 3xx from a billing endpoint is an instruction about where to send a
// credential next. The tool must not take it.
func TestRedirectsAreRefused(t *testing.T) {
	var reached bool
	elsewhere := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		fmt.Fprint(w, `{"balance":1}`)
	}))
	defer elsewhere.Close()

	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL, http.StatusFound)
	}))
	defer ts.Close()

	ep, auth := endpoint(t, ts.URL)
	c := NewWithTransport(ts.Client().Transport, 5*time.Second)
	_, err := c.Get(context.Background(), ep, auth, "sk-test-key-value")
	if err == nil {
		t.Fatal("redirect was followed")
	}
	if !strings.Contains(err.Error(), "refusing to follow redirect") {
		t.Errorf("unexpected error: %v", err)
	}
	if reached {
		t.Fatal("credential was sent to the redirect target")
	}
}

func TestNonJSONIsAClearError(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<html>login page</html>")
	}))
	defer ts.Close()

	ep, auth := endpoint(t, ts.URL)
	c := NewWithTransport(ts.Client().Transport, 5*time.Second)
	_, err := c.Get(context.Background(), ep, auth, "sk-test-key-value")
	if err == nil || !strings.Contains(err.Error(), "not JSON") {
		t.Fatalf("got %v", err)
	}
}

func TestOversizedBodyIsCapped(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"balance":1,"pad":"`))
		chunk := strings.Repeat("x", 64*1024)
		for i := 0; i < 64; i++ { // 4 MiB, four times the cap
			w.Write([]byte(chunk))
		}
		w.Write([]byte(`"}`))
	}))
	defer ts.Close()

	ep, auth := endpoint(t, ts.URL)
	c := NewWithTransport(ts.Client().Transport, 20*time.Second)
	// Truncation makes the JSON invalid, which is the correct outcome: better a
	// clear parse error than an unbounded read.
	if _, err := c.Get(context.Background(), ep, auth, "sk-test-key-value"); err == nil {
		t.Fatal("expected the capped body to fail parsing")
	}
}

// The body is the one part of an HTTP error that may contain the submitted key.
// Callers redact it explicitly; the *default* rendering must be safe anyway,
// because the next caller will be a plain log statement that forgets to.
func TestErrorDoesNotIncludeTheResponseBody(t *testing.T) {
	const key = "sk-test-0123456789abcdef"
	e := &HTTPError{Status: 401, Body: `{"error":"bad key: ` + key + `"}`}
	for _, s := range []string{e.Error(), fmt.Sprintf("%v", error(e)), fmt.Errorf("wrapped: %w", e).Error()} {
		if strings.Contains(s, key) {
			t.Fatalf("the body reached an ordinary error rendering: %s", s)
		}
	}
	if !strings.Contains(e.Error(), "401") {
		t.Errorf("the status is the useful part and is missing: %s", e.Error())
	}
	if got := e.RedactedBody(func(s string) string { return strings.ReplaceAll(s, key, "[REDACTED]") }); !strings.Contains(got, "[REDACTED]") {
		t.Errorf("RedactedBody should still show the redacted message, got %q", got)
	}
}

// A provider error body lands on a terminal. An escape sequence in it could
// repaint what was printed above, including the verified total.
func TestProviderBodyCannotCarryTerminalEscapes(t *testing.T) {
	e := &HTTPError{Status: 402, Body: "\x1b[2J\x1b[Hinsufficient\nbalance"}
	got := e.Redacted(nil)
	if strings.ContainsAny(got, "\x1b\n\r") {
		t.Fatalf("control characters survived: %q", got)
	}
	if !strings.Contains(got, "insufficient balance") {
		t.Errorf("the useful text was lost: %q", got)
	}
}

// `aipocket audit` names the proxy, because a proxy is another host that
// receives the credential. Proxy URLs routinely carry a password, so what is
// named must not include it.
func TestProxyIsReportedWithoutItsCredentials(t *testing.T) {
	via := func(raw string) func(*http.Request) (*url.URL, error) {
		return func(*http.Request) (*url.URL, error) { return url.Parse(raw) }
	}
	got := proxyFor("https://api.example.com/v1/credits", via("http://user:hunter2@proxy.corp:3128"))
	if got == "" {
		t.Fatal("a proxy was not reported at all")
	}
	if strings.Contains(got, "hunter2") || strings.Contains(got, "user") {
		t.Fatalf("the proxy credentials were printed: %q", got)
	}
	if !strings.Contains(got, "proxy.corp:3128") {
		t.Errorf("the proxy host is missing: %q", got)
	}
}

func TestNoProxyMeansNothingIsReported(t *testing.T) {
	none := func(*http.Request) (*url.URL, error) { return nil, nil }
	if got := proxyFor("https://api.example.com/v1/credits", none); got != "" {
		t.Fatalf("ProxyFor = %q, want empty", got)
	}
	// A malformed target is reported as "no proxy" rather than guessed at.
	if got := ProxyFor("not a url"); got != "" {
		t.Fatalf("ProxyFor = %q, want empty", got)
	}
}

func TestHTTPErrorClassifiesAuthFailures(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		e := &HTTPError{Status: code, Body: "nope"}
		if !e.Unauthorized() {
			t.Errorf("%d should classify as unauthorized", code)
		}
	}
	if (&HTTPError{Status: 500}).Unauthorized() {
		t.Error("500 must not classify as unauthorized")
	}
}
