// Package core wires the registry, the credential resolver and the HTTP client
// together and produces Results. Every output surface — table, JSON, MCP,
// guard — renders the same Result slice, so the surfaces cannot drift apart in
// what they claim.
package core

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/naturalmoods/aipocket/internal/fetch"
	"github.com/naturalmoods/aipocket/internal/manifest"
	"github.com/naturalmoods/aipocket/internal/money"
	"github.com/naturalmoods/aipocket/internal/safetext"
	"github.com/naturalmoods/aipocket/internal/secret"
)

// State is the outcome of checking one provider.
type State string

const (
	// StateOK: a balance was read from the provider.
	StateOK State = "ok"
	// StateManual: no balance API; the figure (if any) is user-maintained.
	StateManual State = "manual"
	// StateUnconfigured: no credential available. Not a failure.
	StateUnconfigured State = "unconfigured"
	// StateError: the provider was asked and something went wrong.
	StateError State = "error"
)

// Result is one row of output.
type Result struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	State    State    `json:"state"`
	Balance  *float64 `json:"balance,omitempty"`
	Currency string   `json:"currency,omitempty"`

	// Confidence mirrors the manifest status: whether the number came from a
	// documented field, an undocumented response shape, or a human.
	Confidence manifest.Status `json:"confidence"`

	Detail string `json:"detail,omitempty"` // e.g. "topped up 50.00 / spent 12.35"
	Note   string `json:"note,omitempty"`   // manifest caveats

	// Error is AIPocket's own account of what went wrong: a status code, a
	// transport failure, a schema mismatch. Nothing in it comes from the
	// provider.
	Error string `json:"error,omitempty"`

	// ProviderMessage is what the provider said, redacted and stripped of
	// control characters — and untrusted. It is kept in its own field rather
	// than concatenated into Error so that each surface can decide: the table
	// prints it because a human can weigh "insufficient balance" for themselves,
	// and the MCP server does not forward it, because text of a provider's
	// choosing arriving in a model's context is an instruction channel.
	ProviderMessage string `json:"provider_message,omitempty"`

	KeySource string `json:"key_source,omitempty"` // "env OPENROUTER_API_KEY"
	Console   string `json:"console,omitempty"`
	Spendable *bool  `json:"spendable,omitempty"` // provider says the account can be charged
}

// Report is the full run.
type Report struct {
	GeneratedAt time.Time `json:"generated_at"`
	Results     []Result  `json:"providers"`

	// TotalVerified sums only USD balances read from a *documented* field
	// (confidence "official"). Everything else is kept out and surfaced
	// separately.
	//
	// The separation is the product. A figure inferred from an undocumented
	// response shape, a figure a human typed into the config, and a figure a
	// provider documents are three different epistemic objects; adding them
	// into one number is precisely the dishonesty this tool exists to avoid.
	// Non-USD balances are excluded rather than converted, because AIPocket has
	// no business guessing an exchange rate.
	TotalVerified float64 `json:"total_verified_usd"`
	// TotalInferred sums USD balances whose provider is marked "undocumented".
	TotalInferred float64 `json:"total_inferred_usd,omitempty"`
	// TotalManual sums user-maintained figures.
	TotalManual float64  `json:"total_manual_usd,omitempty"`
	Excluded    []string `json:"excluded_from_verified_total,omitempty"`
}

// Checker runs balance checks.
type Checker struct {
	Registry *manifest.Registry
	Config   *Config
	Client   *fetch.Client
	Redactor *secret.Redactor
}

// NewChecker builds a Checker with sane defaults.
func NewChecker(reg *manifest.Registry, cfg *Config, timeout time.Duration) *Checker {
	return &Checker{
		Registry: reg,
		Config:   cfg,
		Client:   fetch.New(timeout),
		Redactor: &secret.Redactor{},
	}
}

// Selected returns the providers to check: everything not disabled, or the
// explicit subset if ids is non-empty.
func (c *Checker) Selected(ids []string) ([]*manifest.Provider, error) {
	if len(ids) == 0 {
		var out []*manifest.Provider
		for _, p := range c.Registry.All() {
			if !c.Config.Providers[p.ID].Disabled {
				out = append(out, p)
			}
		}
		return out, nil
	}
	var out []*manifest.Provider
	for _, id := range ids {
		p, ok := c.Registry.Get(id)
		if !ok {
			return nil, fmt.Errorf("unknown provider %q (see `aipocket providers`)", id)
		}
		// Naming a disabled provider explicitly must not quietly contact it:
		// `aipocket audit` omits disabled providers, so honouring the request
		// would make audit's promise false.
		if c.Config.Providers[p.ID].Disabled {
			return nil, fmt.Errorf("provider %q is disabled in your config", id)
		}
		out = append(out, p)
	}
	return out, nil
}

// Run checks providers concurrently and assembles a Report.
func (c *Checker) Run(ctx context.Context, providers []*manifest.Provider) Report {
	results := make([]Result, len(providers))
	var wg sync.WaitGroup
	for i, p := range providers {
		wg.Add(1)
		go func(i int, p *manifest.Provider) {
			defer wg.Done()
			results[i] = c.checkOne(ctx, p)
		}(i, p)
	}
	wg.Wait()

	sort.Slice(results, func(i, j int) bool { return results[i].ID < results[j].ID })

	rep := Report{GeneratedAt: time.Now().UTC(), Results: results}
	for i := range results {
		r := &results[i]
		if r.Balance == nil {
			continue
		}
		// Last gate before the number is summed. Every producer checks its own
		// figure — jpath on parse, manifest on the total-minus-used difference,
		// LoadConfig on a `manual:` value — and this catches whatever a future
		// producer forgets, because the cost of missing one is not a wrong row
		// but an unencodable report and every other provider's balance erased.
		if !money.Plausible(*r.Balance) {
			r.State, r.Balance = StateError, nil
			r.Error = "the figure read for this provider is not a usable amount of " +
				"money; it is withheld rather than reported or totalled"
			continue
		}
		switch {
		case r.State == StateManual:
			rep.TotalManual += *r.Balance
			rep.Excluded = append(rep.Excluded, r.ID+" (user-maintained)")
		case r.State != StateOK:
			// nothing to total
		case r.Currency != "USD":
			rep.Excluded = append(rep.Excluded,
				fmt.Sprintf("%s (%s, not converted)", r.ID, r.Currency))
		case r.Confidence == manifest.StatusOfficial:
			rep.TotalVerified += *r.Balance
		default:
			rep.TotalInferred += *r.Balance
			rep.Excluded = append(rep.Excluded, r.ID+" (inferred field)")
		}
	}
	return rep
}

func (c *Checker) checkOne(ctx context.Context, p *manifest.Provider) Result {
	pc := c.Config.Providers[p.ID]
	res := Result{
		ID:         p.ID,
		Name:       p.Name,
		Confidence: p.Status,
		Note:       p.Notes,
		Console:    p.Console,
	}

	spec := pc.Key
	if spec == "" {
		spec = "env:" + p.Auth.Env
	}
	src, err := secret.Parse(spec)
	if err != nil {
		res.State, res.Error = StateError, err.Error()
		return res
	}
	res.KeySource = src.Describe()

	key, err := src.Resolve(ctx)
	if err != nil {
		var nc *secret.ErrNotConfigured
		if errors.As(err, &nc) {
			res.State = StateUnconfigured
			res.Error = nc.Detail
			// A manual figure is still worth showing for an unconfigured
			// provider — that is exactly the case it exists for.
			if pc.Manual != nil {
				res.State, res.Balance, res.Currency = StateManual, pc.Manual, "USD"
				res.Confidence = manifest.StatusNoAPI
				res.Error = ""
				res.Detail = "user-maintained figure"
			}
			return res
		}
		res.State = StateError
		res.Error, res.ProviderMessage = c.describe(err)
		return res
	}
	c.Redactor.Add(key)

	// Providers with no balance API: verify the key works, then fall back to
	// whatever the user tracks by hand.
	if p.Balance == nil {
		res.State = StateManual
		res.Confidence = manifest.StatusNoAPI
		if pc.Manual != nil {
			res.Balance, res.Currency = pc.Manual, "USD"
		}
		if p.Verify != nil {
			if _, err := c.Client.Get(ctx, p.Verify, p.Auth, key); err != nil {
				msg, provider := c.describe(err)
				res.Detail = "key check failed: " + msg
				res.ProviderMessage = provider
			} else {
				res.Detail = "key valid"
			}
		}
		if pc.Manual != nil {
			res.Detail = joinDetail(res.Detail, "user-maintained figure")
		}
		return res
	}

	ep := p.Balance
	if pc.BalanceURL != "" {
		// Copy so the override does not leak into other runs of the registry.
		override := *ep
		override.URL = pc.BalanceURL
		ep = &override
		res.Detail = "endpoint overridden in config"
	}

	doc, err := c.Client.Get(ctx, ep, p.Auth, key)
	if err != nil {
		res.State = StateError
		res.Error, res.ProviderMessage = c.describe(err)
		var he *fetch.HTTPError
		if errors.As(err, &he) && he.Unauthorized() {
			res.Error += " — the key may be valid for inference but not for billing"
		}
		return res
	}

	if ap := ep.AvailablePath(); ap != nil {
		if v, ok := ap.Eval(doc); ok {
			if b, isBool := v.(bool); isBool {
				res.Spendable = &b
				if !b {
					res.Detail = joinDetail(res.Detail, "provider reports the account cannot be charged")
				}
			}
		}
	}

	for _, amt := range ep.Amounts {
		v, ok := amt.Resolve(doc)
		if !ok {
			continue
		}
		// A prepaid balance derived as total-minus-used cannot be negative. If
		// it is, the two fields have most likely swapped meaning in a schema
		// change, and reporting -37.65 with a green "ok" would both look
		// authoritative and silently reduce every other provider's total.
		if amt.IsDerived() && v < 0 {
			res.State = StateError
			res.Error = fmt.Sprintf(
				"computed a negative balance (%.2f) from total minus used, which is "+
					"impossible for prepaid credit — the response shape probably "+
					"changed; please open an issue", v)
			return res
		}
		res.State = StateOK
		res.Balance = &v
		res.Currency = amt.Currency
		res.Detail = joinDetail(res.Detail, amt.Detail(doc))
		if amt.Label != "" {
			res.Detail = joinDetail(res.Detail, amt.Label)
		}
		return res
	}

	res.State = StateError
	res.Error = "the provider answered but no balance field matched — " +
		"the response shape probably changed; please open an issue"
	return res
}

// describe renders an error for display, separating the tool's account of the
// failure from the provider's.
//
// Redaction is applied to the full text before any shortening, so a secret
// cannot survive as a truncated prefix; control characters are stripped after
// that, because this text reaches a terminal.
func (c *Checker) describe(err error) (msg, provider string) {
	var he *fetch.HTTPError
	if errors.As(err, &he) {
		return he.Summary(), he.RedactedBody(c.Redactor.Apply)
	}
	return safetext.Sanitize(c.Redactor.Apply(err.Error())), ""
}

func joinDetail(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return ""
	}
	s := out[0]
	for _, p := range out[1:] {
		s += "; " + p
	}
	return s
}
