// Package fetch performs the one network operation AIPocket needs: an
// authenticated GET against a host named in a manifest.
//
// The transport is deliberately restrictive, because every request carries a
// credential that can spend money:
//
//   - https only, enforced again here and not just at manifest load time;
//   - redirects are never followed. A 3xx from a billing endpoint is treated as
//     an error rather than an instruction about where to send the key next;
//   - response bodies are size-capped, so a hostile or broken endpoint cannot
//     exhaust memory;
//   - every request has a deadline;
//   - error bodies are provider-controlled text. They are useful enough to show,
//     so they are redacted and stripped of control characters on the way out —
//     and HTTPError.Error() does not include them at all, so an ordinary log
//     statement cannot leak one.
//
// The default transport honours the proxy environment variables, which means an
// HTTPS_PROXY changes which host actually receives the credential. Breaking
// every corporate network to avoid that would be the wrong trade, so ProxyFor
// exists and `aipocket audit` names the proxy instead.
package fetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/naturalmoods/aipocket/internal/manifest"
	"github.com/naturalmoods/aipocket/internal/safetext"
)

const (
	maxBody        = 1 << 20 // 1 MiB is orders of magnitude above any billing response
	defaultTimeout = 20 * time.Second
)

// Version is overwritten at build time via -ldflags and appears in the
// User-Agent, so providers can identify (and if necessary contact) the client.
// It is empty in a build that did not set it; use ResolvedVersion, which falls
// back to the version Go itself recorded.
var Version = ""

var resolvedVersion = sync.OnceValue(func() string {
	if Version != "" {
		return Version
	}
	// `go install github.com/naturalmoods/aipocket/cmd/aipocket@latest` passes no
	// ldflags, so the binary used to call itself "dev" — which made the one
	// question that matters after a security advisory ("am I running an affected
	// version?") unanswerable, both for the user and for a provider reading the
	// User-Agent. Go records the module version for exactly this case.
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	// A local `go build` has no module version, but it does have the commit.
	var rev string
	var dirty bool
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return "dev"
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if dirty {
		return "devel-" + rev + "-dirty"
	}
	return "devel-" + rev
})

// ResolvedVersion is the version this binary reports: the value injected at
// build time if there is one, otherwise whatever Go recorded about the build.
func ResolvedVersion() string { return resolvedVersion() }

func userAgent() string { return "aipocket/" + ResolvedVersion() }

// ProxyFor reports the proxy the default transport would send rawURL through,
// or "" if there is none.
//
// `aipocket audit` promises the complete network footprint, and an implicit
// HTTPS_PROXY changes which host actually receives the credential — the manifest
// host becomes a name in a CONNECT request instead. Refusing to honour the
// proxy would break every corporate network; naming it keeps audit's promise
// true. Any userinfo is dropped: proxy URLs routinely carry a password.
func ProxyFor(rawURL string) string { return proxyFor(rawURL, http.ProxyFromEnvironment) }

// proxyFor takes the resolver as an argument because http.ProxyFromEnvironment
// reads the environment exactly once per process and caches it, which is right
// for a real run and untestable in a second test.
func proxyFor(rawURL string, resolve func(*http.Request) (*url.URL, error)) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return ""
	}
	p, err := resolve(&http.Request{URL: u, Header: http.Header{}})
	if err != nil || p == nil {
		return ""
	}
	shown := *p
	shown.User = nil
	return safetext.Sanitize(shown.String())
}

// Client performs manifest-driven requests.
type Client struct {
	http *http.Client
}

// New builds a Client with the default transport.
func New(timeout time.Duration) *Client { return NewWithTransport(nil, timeout) }

// NewWithTransport builds a Client over a caller-supplied RoundTripper. It
// exists for tests and for environments that need a corporate proxy or a
// pinned CA pool. The redirect and timeout policies are applied either way —
// they are not the caller's to opt out of.
func NewWithTransport(rt http.RoundTripper, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Client{http: &http.Client{
		Transport: rt,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return fmt.Errorf("refusing to follow redirect to %s", req.URL.Host)
		},
	}}
}

// HTTPError carries a non-2xx response. The body is kept because provider error
// messages are genuinely useful ("insufficient balance", "key revoked"), but it
// is untrusted, unredacted bytes — reach it only through RedactedBody.
type HTTPError struct {
	Status int
	Body   string
}

// Error deliberately omits the body.
//
// It used to include it, with every caller expected to remember to redact
// separately. That works exactly until someone logs the error the ordinary way —
// `log.Printf("%v", err)`, a wrapped `%w` chain printed higher up — and a
// provider that echoes the submitted key in its 401 body puts that key in the
// log. The safe rendering is the default one; a caller that wants the body has
// to ask for it, and asking means supplying a redactor.
func (e *HTTPError) Error() string {
	return e.Summary() + " (response body withheld; use RedactedBody to display it)"
}

// Summary is the tool's own description of the failure: a status code and its
// standard meaning, with nothing the provider wrote.
func (e *HTTPError) Summary() string {
	if text := http.StatusText(e.Status); text != "" {
		return fmt.Sprintf("HTTP %d %s", e.Status, text)
	}
	return fmt.Sprintf("HTTP %d", e.Status)
}

// RedactedBody renders the provider's own message, ready for display: redacted,
// stripped of control characters, whitespace collapsed and then shortened. It is
// "" when the provider said nothing.
//
// The ordering is the entire point. Truncating first and redacting second lets
// a key that straddles the cut survive as a prefix: the redactor matches whole
// values, and half a key is no longer a whole value. That is not hypothetical —
// a provider 401 body that echoes the submitted key is exactly the case this
// guards, and it is how a key prefix reaches a terminal or a CI log. Sanitising
// comes after redaction for the same reason: rewriting the text first could
// split a secret across the substitution.
func (e *HTTPError) RedactedBody(redact func(string) string) string {
	body := e.Body
	if redact != nil {
		body = redact(body)
	}
	body = safetext.Sanitize(body)
	body = strings.Join(strings.Fields(body), " ")
	return truncateRunes(body, 300)
}

// Redacted is Summary plus the provider's message, for surfaces that show both
// as one string.
func (e *HTTPError) Redacted(redact func(string) string) string {
	if body := e.RedactedBody(redact); body != "" {
		return e.Summary() + ": " + body
	}
	return e.Summary()
}

// truncateRunes cuts on a rune boundary; slicing bytes would emit a broken
// UTF-8 sequence for a provider that answers in a non-ASCII language.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// Unauthorized reports whether the status indicates a bad or unprivileged key,
// which callers present differently from a transport failure.
func (e *HTTPError) Unauthorized() bool {
	return e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden
}

// Get calls ep with the supplied credential and decodes the JSON response.
func (c *Client) Get(ctx context.Context, ep *manifest.Endpoint, auth manifest.Auth, key string) (any, error) {
	if !strings.HasPrefix(ep.URL, "https://") {
		return nil, errors.New("refusing to send a credential over a non-https url")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep.URL, nil)
	if err != nil {
		return nil, err
	}
	// A manifest's static headers go on first, and the credential second, so the
	// credential header always wins. Manifest validation already refuses a static
	// header that would displace it — this ordering means a bug in that check
	// still cannot stop the key from being sent correctly, or send a manifest's
	// literal in its place.
	for name, value := range auth.Headers {
		req.Header.Set(name, value)
	}
	switch auth.Type {
	case "bearer":
		req.Header.Set("Authorization", "Bearer "+key)
	case "header":
		req.Header.Set(auth.Header, key)
	default:
		return nil, fmt.Errorf("unsupported auth type %q", auth.Type)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent())

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &HTTPError{Status: resp.StatusCode, Body: string(body)}
	}

	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("response was not JSON (wrong endpoint?)")
	}
	return doc, nil
}
