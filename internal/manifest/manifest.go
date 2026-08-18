// Package manifest defines the declarative provider format.
//
// A provider is data, not code. Adding one is a single YAML file in
// providers/ — no Go changes, no new code paths to review. That is the whole
// point of the design: the set of providers changes weekly, the engine does
// not, so contributors should never have to touch the engine.
//
// The security consequence of "providers are data" is that a manifest tells
// the tool where to send an API key. Manifests are therefore embedded into the
// binary at build time and are never fetched at runtime; see the package
// comment in internal/fetch for the transport-level restrictions.
package manifest

import (
	"fmt"
	"io/fs"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/naturalmoods/aipocket/internal/jpath"
	"github.com/naturalmoods/aipocket/internal/money"
	"github.com/naturalmoods/aipocket/internal/secret"
)

// Status describes how much the tool can actually promise about a number.
// It is surfaced in every output format, because a balance read out of an
// undocumented response shape is not the same kind of fact as one read out of
// a documented field, and collapsing the two would be dishonest.
type Status string

const (
	// StatusOfficial: the provider documents both the endpoint and the field.
	StatusOfficial Status = "official"
	// StatusUndocumented: the endpoint is documented, the response shape is not.
	StatusUndocumented Status = "undocumented"
	// StatusNoAPI: no balance endpoint exists; the tool can only verify the key.
	StatusNoAPI Status = "no-api"
)

// Provider is one entry in the registry.
type Provider struct {
	ID      string `yaml:"id"`
	Name    string `yaml:"name"`
	Status  Status `yaml:"status"`
	Console string `yaml:"console"` // where a human checks it by hand
	Docs    string `yaml:"docs"`    // upstream API documentation
	Notes   string `yaml:"notes"`

	Auth    Auth      `yaml:"auth"`
	Balance *Endpoint `yaml:"balance"`
	// Verify is used when Balance is nil: it proves the key works even though
	// the provider exposes no balance.
	Verify *Endpoint `yaml:"verify"`
}

// Auth describes how the credential is attached to the request.
type Auth struct {
	// Type is "bearer" or "header".
	Type string `yaml:"type"`
	// Header is the header name for Type=="header".
	Header string `yaml:"header"`
	// Scheme is the word before the credential in an Authorization header, for
	// Type=="bearer". Empty means "Bearer", which is what nearly every provider
	// wants; fal documents `Authorization: Key <key>`, and a scheme is the whole
	// difference between reading its balance and not being able to.
	Scheme string `yaml:"scheme"`
	// Headers are static headers a provider requires on every request.
	// Anthropic's /v1/models answers 400 without anthropic-version, which would
	// make the tool report "key check failed" for a perfectly good key — being
	// confidently wrong about the one thing it can check for a no-api provider.
	//
	// Literal values only, and never a secret. A header whose value is a
	// credential is a second credential, and then secret.Redactor, `aipocket
	// audit` and `aipocket providers` all have to know about it; that is its own
	// design, not an extension of this field.
	Headers map[string]string `yaml:"headers"`
	// Env is the conventional environment variable, used as the default
	// credential source when the config file says nothing.
	Env string `yaml:"env"`
}

// schemePattern is what may stand before a credential in an Authorization
// header: one token, nothing that could end the header line and start another.
// This value is written directly next to a key, so the rule is mechanical rather
// than left to a reviewer noticing.
var schemePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]*$`)

// AuthScheme is the word to write before the credential, defaulting to Bearer.
func (a Auth) AuthScheme() string {
	if a.Scheme == "" {
		return "Bearer"
	}
	return a.Scheme
}

// headerNamePattern is what a manifest may name as a static header.
// Deliberately narrower than the HTTP grammar: a data file must not be able to
// put a colon, a CR or an LF into a request.
var headerNamePattern = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

// reservedHeaders are the ones fetch sets itself. A manifest naming one would
// either be silently ignored (Accept, User-Agent — and a User-Agent a provider
// could not attribute defeats the point of sending one) or would displace the
// credential (Authorization). Both are worse than refusing to load.
var reservedHeaders = map[string]bool{
	"authorization": true,
	"accept":        true,
	"user-agent":    true,
}

func (a Auth) validateHeaders() error {
	names := make([]string, 0, len(a.Headers))
	for name := range a.Headers {
		names = append(names, name)
	}
	// Sorted so that a manifest with two bad headers always reports the same one
	// first; map order would make the error message depend on the run.
	sort.Strings(names)

	for _, name := range names {
		switch {
		case !headerNamePattern.MatchString(name):
			return fmt.Errorf("auth.headers: %q is not a usable header name", name)
		case reservedHeaders[strings.ToLower(name)]:
			return fmt.Errorf("auth.headers: %s is set by aipocket itself and cannot be overridden here", name)
		case a.Header != "" && strings.EqualFold(name, a.Header):
			return fmt.Errorf("auth.headers: %s carries the credential and cannot be set here", name)
		}
		// The value is not quoted back on failure. It is not supposed to be a
		// secret, but "not supposed to be" is exactly the case where echoing it
		// would be the mistake, and the name alone identifies the line to fix.
		if !printableASCII(a.Headers[name]) {
			return fmt.Errorf("auth.headers: the value of %s must be non-empty printable ASCII", name)
		}
	}
	return nil
}

func printableASCII(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r > 0x7e {
			return false
		}
	}
	return true
}

// Endpoint is a single GET the tool may perform.
type Endpoint struct {
	URL string `yaml:"url"`
	// Amounts are candidate readings, tried in order; the first one that
	// resolves is reported. This models providers that hold several balances
	// (DeepSeek keeps separate CNY and USD wallets) without inventing an
	// expression language.
	Amounts []Amount `yaml:"amounts"`
	// Available is an optional boolean path; when it resolves to false the
	// account cannot be charged even if the balance looks positive.
	Available string `yaml:"available"`

	availablePath *jpath.Path
	host          string
}

// Amount is one way to read a balance out of the response.
//
// Either Path (the remaining balance directly) or the Total/Used pair must be
// set. Total minus Used covers providers that report lifetime figures instead
// of a remaining balance, which is the common case.
type Amount struct {
	Label    string `yaml:"label"`
	Path     string `yaml:"path"`
	Total    string `yaml:"total"`
	Used     string `yaml:"used"`
	Currency string `yaml:"currency"`

	// Scale converts the provider's unit into the currency's own. Novita reports
	// integers in 1/10000 USD, so 0.0001 turns 1000000 into the $100 it means;
	// without it the row would claim a million dollars and money.Plausible would
	// not object, because a million dollars is a plausible amount of money.
	//
	// A pointer so that `scale: 0` is distinguishable from no scale at all. The
	// first is refused — it would report every account as empty — and a setting
	// that silently means 1 is exactly what this format does not do.
	Scale *float64 `yaml:"scale"`

	path, total, used *jpath.Path
}

// Host returns the single host this endpoint contacts. Used by `aipocket audit`
// so a user can see the full network footprint before running anything.
func (e *Endpoint) Host() string { return e.host }

// AvailablePath returns the compiled availability path, or nil.
func (e *Endpoint) AvailablePath() *jpath.Path { return e.availablePath }

// IsDerived reports whether the amount is computed as total minus used, as
// opposed to read directly. Only a derived amount can be checked for the
// impossible-negative case: a direct field could legitimately be negative if a
// provider models an overdraft.
func (a Amount) IsDerived() bool { return a.path == nil }

// Resolve reads the first amount that the response satisfies.
func (a Amount) Resolve(doc any) (value float64, ok bool) {
	if a.path != nil {
		v, ok := a.path.Number(doc)
		if !ok {
			return 0, false
		}
		return a.scaled(v)
	}
	total, tok := a.total.Number(doc)
	used, uok := a.used.Number(doc)
	if !tok || !uok {
		return 0, false
	}
	// Both operands passed the money check individually, which is not the same
	// as their difference passing it: two figures just inside the bound subtract
	// to one twice as far outside it. Every figure the tool reports is checked
	// where it is produced, not only where it is read.
	//
	// Scaling the difference is the same arithmetic as scaling both operands, and
	// it puts the check after the conversion — where the figure that will actually
	// be reported finally exists.
	return a.scaled(total - used)
}

// scaled applies the amount's unit conversion and gates the result.
//
// The order is the point: money.Plausible runs on the scaled figure, because that
// is the one that reaches the report. Checking before the conversion would refuse
// a legitimate reading whose raw units are large and pass a scaled one that is
// not money at all.
func (a Amount) scaled(v float64) (float64, bool) {
	if a.Scale != nil {
		v *= *a.Scale
	}
	if !money.Plausible(v) {
		return 0, false
	}
	return v, true
}

// Detail renders the underlying figures for an amount derived from
// total-minus-used, so the table can show its work.
func (a Amount) Detail(doc any) string {
	if a.path != nil || a.total == nil {
		return ""
	}
	total, tok := a.total.Number(doc)
	used, uok := a.used.Number(doc)
	if !tok || !uok {
		return ""
	}
	return fmt.Sprintf("topped up %.2f / spent %.2f", total, used)
}

// Registry is the loaded, validated set of providers.
type Registry struct {
	providers map[string]*Provider
	order     []string
}

// Load parses and validates a registry from fsys/dir. Validation happens once,
// at startup, so a malformed manifest is a build/test failure rather than a
// surprise in the middle of a run.
func Load(fsys fs.FS, dir string) (*Registry, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	r := &Registry{providers: map[string]*Provider{}}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		raw, err := fs.ReadFile(fsys, dir+"/"+e.Name())
		if err != nil {
			return nil, err
		}
		var p Provider
		dec := yaml.NewDecoder(strings.NewReader(string(raw)))
		dec.KnownFields(true) // a typo in a manifest must not silently no-op
		if err := dec.Decode(&p); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		if err := p.validate(); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		if _, dup := r.providers[p.ID]; dup {
			return nil, fmt.Errorf("%s: duplicate provider id %q", e.Name(), p.ID)
		}
		r.providers[p.ID] = &p
		r.order = append(r.order, p.ID)
	}
	if len(r.order) == 0 {
		return nil, fmt.Errorf("no provider manifests found in %s", dir)
	}
	sort.Strings(r.order)
	return r, nil
}

// All returns providers in stable ID order.
func (r *Registry) All() []*Provider {
	out := make([]*Provider, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.providers[id])
	}
	return out
}

// Get returns a provider by ID.
func (r *Registry) Get(id string) (*Provider, bool) {
	p, ok := r.providers[strings.ToLower(id)]
	return p, ok
}

// IDs returns all known provider IDs.
func (r *Registry) IDs() []string { return append([]string(nil), r.order...) }

func (p *Provider) validate() error {
	if p.ID == "" || p.ID != strings.ToLower(p.ID) {
		return fmt.Errorf("id must be present and lowercase")
	}
	if p.Name == "" {
		return fmt.Errorf("name is required")
	}
	switch p.Status {
	case StatusOfficial, StatusUndocumented, StatusNoAPI:
	default:
		return fmt.Errorf("status must be official, undocumented or no-api")
	}
	switch p.Auth.Type {
	case "bearer":
		if p.Auth.Scheme != "" && !schemePattern.MatchString(p.Auth.Scheme) {
			return fmt.Errorf("auth.scheme %q must be a single token of letters, digits and hyphens", p.Auth.Scheme)
		}
	case "header":
		if p.Auth.Header == "" {
			return fmt.Errorf("auth.header is required when auth.type is header")
		}
		// A scheme has no meaning here — the credential is written raw into the
		// named header — and a setting that silently does nothing is the failure
		// mode this format refuses everywhere else.
		if p.Auth.Scheme != "" {
			return fmt.Errorf("auth.scheme applies to auth.type bearer, not header")
		}
	default:
		return fmt.Errorf("auth.type must be bearer or header")
	}
	if p.Auth.Env == "" {
		return fmt.Errorf("auth.env is required")
	}
	// The manifest's variable name becomes a credential spec ("env:" + Env) and
	// is printed by `aipocket providers` and `aipocket audit`, so it is held to
	// exactly the rule user config is held to. Validating it here means the
	// default spec can never be one Parse would refuse.
	if !secret.ValidEnvName(p.Auth.Env) {
		return fmt.Errorf("auth.env %q must be an upper-case environment variable name", p.Auth.Env)
	}
	if err := p.Auth.validateHeaders(); err != nil {
		return err
	}
	if p.Balance == nil && p.Verify == nil {
		return fmt.Errorf("a provider needs either a balance or a verify endpoint")
	}
	if p.Status == StatusNoAPI && p.Balance != nil {
		return fmt.Errorf("status no-api must not define a balance endpoint")
	}
	if p.Status != StatusNoAPI && p.Balance == nil {
		return fmt.Errorf("status %s requires a balance endpoint", p.Status)
	}
	for _, ep := range []*Endpoint{p.Balance, p.Verify} {
		if ep == nil {
			continue
		}
		if err := ep.compile(ep == p.Balance); err != nil {
			return err
		}
	}
	return nil
}

func (e *Endpoint) compile(needAmounts bool) error {
	u, err := url.Parse(e.URL)
	if err != nil {
		return fmt.Errorf("bad url %q: %w", e.URL, err)
	}
	// https only, and no credentials smuggled into the URL. A manifest is the
	// one place an attacker could redirect a key, so the rules are mechanical
	// and enforced at load time rather than left to review.
	if u.Scheme != "https" {
		return fmt.Errorf("url %q must be https", e.URL)
	}
	if u.Host == "" {
		return fmt.Errorf("url %q has no host", e.URL)
	}
	if u.User != nil {
		return fmt.Errorf("url %q must not contain userinfo", e.URL)
	}
	e.host = u.Host

	if e.Available != "" {
		p, err := jpath.Compile(e.Available)
		if err != nil {
			return err
		}
		e.availablePath = &p
	}

	if !needAmounts {
		if len(e.Amounts) > 0 {
			return fmt.Errorf("verify endpoint must not define amounts")
		}
		return nil
	}
	if len(e.Amounts) == 0 {
		return fmt.Errorf("balance endpoint needs at least one amount")
	}
	for i := range e.Amounts {
		a := &e.Amounts[i]
		if a.Currency == "" {
			return fmt.Errorf("amount %d: currency is required", i)
		}
		// NaN fails the > 0 comparison, so this covers zero, negatives and NaN;
		// the Inf check is separate because +Inf is greater than zero.
		if a.Scale != nil && (!(*a.Scale > 0) || math.IsInf(*a.Scale, 0)) {
			return fmt.Errorf("amount %d: scale must be a finite number greater than zero", i)
		}
		switch {
		case a.Path != "" && (a.Total != "" || a.Used != ""):
			return fmt.Errorf("amount %d: set either path or total/used, not both", i)
		case a.Path != "":
			p, err := jpath.Compile(a.Path)
			if err != nil {
				return err
			}
			a.path = &p
		case a.Total != "" && a.Used != "":
			t, err := jpath.Compile(a.Total)
			if err != nil {
				return err
			}
			u, err := jpath.Compile(a.Used)
			if err != nil {
				return err
			}
			a.total, a.used = &t, &u
		default:
			return fmt.Errorf("amount %d: needs path, or both total and used", i)
		}
	}
	return nil
}
