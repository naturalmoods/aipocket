// Command aipocket reports remaining prepaid credit at LLM API providers.
//
// Exit codes: 0 success, 1 at least one provider errored, 2 usage error.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	neturl "net/url"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/naturalmoods/aipocket"
	"github.com/naturalmoods/aipocket/internal/core"
	"github.com/naturalmoods/aipocket/internal/fetch"
	"github.com/naturalmoods/aipocket/internal/manifest"
	"github.com/naturalmoods/aipocket/internal/mcp"
	"github.com/naturalmoods/aipocket/internal/render"
	"github.com/naturalmoods/aipocket/internal/secret"
)

// version is set at build time: -ldflags "-X main.version=v1.2.3". Empty in a
// build that did not set it; appVersion falls back to what Go recorded, so a
// `go install …@latest` binary can still say which release it is.
var version = ""

func appVersion() string {
	if version != "" {
		return version
	}
	return fetch.ResolvedVersion()
}

const usage = `aipocket — remaining prepaid credit at your LLM API providers

usage:
  aipocket [flags] [provider...]      check balances (default)
  aipocket providers                  list known providers
  aipocket audit                      show every host that would be contacted
  aipocket probe <provider>           search for an undocumented balance endpoint
  aipocket mcp                        run as an MCP server on stdio
  aipocket config path                print the config file location
  aipocket version

flags:
  --json            machine-readable output
  --no-color        disable colour (also honours NO_COLOR)
  --timeout DUR     per-request timeout (default 20s)
  --config PATH     config file (default: see 'aipocket config path')
  --dry-run         (probe) list the requests it would make, send nothing

AIPocket never stores credentials. The config file holds instructions for
obtaining them:

  providers:
    openrouter:
      key: command:op read op://Private/OpenRouter/credential
    deepseek:
      key: env:DEEPSEEK_API_KEY

With no config at all, each provider's conventional environment variable is
used. Run 'aipocket providers' to see them.
`

func main() {
	os.Exit(run())
}

func run() int {
	fs := flag.NewFlagSet("aipocket", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }

	var (
		asJSON     = fs.Bool("json", false, "machine-readable output")
		noColor    = fs.Bool("no-color", false, "disable colour output")
		timeout    = fs.Duration("timeout", 20*time.Second, "per-request timeout")
		configPath = fs.String("config", "", "config file path")
		dryRun     = fs.Bool("dry-run", false, "probe: list the requests, send nothing")
	)
	args, err := parseArgs(fs, os.Args[1:])
	if err != nil {
		return 2
	}

	cmd := "check"
	if len(args) > 0 && isCommand(args[0]) {
		cmd, args = args[0], args[1:]
	}

	reg, err := aipocket.Registry()
	if err != nil {
		// A broken embedded registry is a build defect, not a user error.
		fmt.Fprintf(os.Stderr, "aipocket: registry is invalid: %v\n", err)
		return 1
	}

	path := *configPath
	if path == "" {
		path, err = core.ConfigPath()
		if err != nil {
			fmt.Fprintf(os.Stderr, "aipocket: %v\n", err)
			return 1
		}
	}
	cfg, err := core.LoadConfig(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aipocket: %v\n", err)
		return 2
	}
	// An id the registry does not know is a typo, and a typo here is the kind
	// that looks like it worked: `openruter: {disabled: true}` reports nothing
	// wrong while openrouter keeps being contacted with the key the user thought
	// they had withdrawn.
	if err := cfg.ValidateAgainst(reg); err != nil {
		fmt.Fprintf(os.Stderr, "aipocket: %s: %v\n", path, err)
		return 2
	}

	// --dry-run only means something to probe, and a flag whose purpose is to stop
	// the tool from sending a credential must not be silently ignored: someone who
	// types `aipocket --dry-run` is asking for nothing to be sent.
	if *dryRun && cmd != "probe" {
		fmt.Fprintf(os.Stderr, "aipocket: --dry-run applies to `aipocket probe`, not to `aipocket %s`\n", cmd)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch cmd {
	case "version":
		fmt.Printf("aipocket %s\n", appVersion())
		return 0

	case "config":
		if len(args) == 0 || args[0] != "path" {
			fmt.Fprint(os.Stderr, "usage: aipocket config path\n")
			return 2
		}
		fmt.Println(path)
		return 0

	case "providers":
		return listProviders(reg, cfg, *asJSON)

	case "audit":
		return audit(reg, cfg, *asJSON)

	case "probe":
		if len(args) != 1 {
			fmt.Fprint(os.Stderr, "usage: aipocket probe <provider>\n")
			return 2
		}
		return probe(ctx, reg, cfg, args[0], *timeout, *dryRun)

	case "mcp":
		checker := core.NewChecker(reg, cfg, *timeout)
		srv := &mcp.Server{Checker: checker, Version: appVersion(), Timeout: 2 * time.Minute}
		if err := srv.Serve(ctx, os.Stdin, os.Stdout, os.Stderr); err != nil {
			fmt.Fprintf(os.Stderr, "aipocket: mcp: %v\n", err)
			return 1
		}
		return 0

	case "check":
		return check(ctx, reg, cfg, args, *timeout, *asJSON, useColor(*noColor))
	}

	fs.Usage()
	return 2
}

// parseArgs parses flags that may appear anywhere on the command line and
// returns the positional arguments in order.
//
// The flag package stops at the first non-flag argument, which for this CLI is
// silent misbehaviour rather than a limitation: `aipocket openrouter --json`
// printed a table and ignored --json, and `aipocket probe deepseek --dry-run`
// took --dry-run for a second provider name — a flag whose entire purpose is to
// stop the tool from sending a credential. Resuming the parse after each
// positional makes flag position irrelevant.
func parseArgs(fs *flag.FlagSet, argv []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(argv); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		positional = append(positional, rest[0])
		argv = rest[1:]
	}
}

func isCommand(s string) bool {
	switch s {
	case "check", "providers", "audit", "probe", "mcp", "config", "version", "help":
		return true
	}
	return false
}

func check(ctx context.Context, reg *manifest.Registry, cfg *core.Config,
	ids []string, timeout time.Duration, asJSON, color bool) int {

	checker := core.NewChecker(reg, cfg, timeout)
	providers, err := checker.Selected(ids)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aipocket: %v\n", err)
		return 2
	}
	rep := checker.Run(ctx, providers)

	if asJSON {
		if err := render.JSON(os.Stdout, rep); err != nil {
			fmt.Fprintf(os.Stderr, "aipocket: %v\n", err)
			return 1
		}
	} else {
		render.Table(os.Stdout, rep, color)
	}

	for _, r := range rep.Results {
		if r.State == core.StateError {
			return 1
		}
	}
	return 0
}

func listProviders(reg *manifest.Registry, cfg *core.Config, asJSON bool) int {
	type row struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Confidence string `json:"confidence"`
		Env        string `json:"env"`
		KeySource  string `json:"key_source"`
		Console    string `json:"console"`
	}
	var rows []row
	for _, p := range reg.All() {
		rows = append(rows, row{p.ID, p.Name, string(p.Status), p.Auth.Env,
			keySource(credentialSpec(p, cfg)), p.Console})
	}
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rows)
		return 0
	}
	var wID, wConf, wEnv int
	for _, r := range rows {
		wID, wConf, wEnv = maxInt(wID, len(r.ID)), maxInt(wConf, len(r.Confidence)), maxInt(wEnv, len(r.Env))
	}
	fmt.Println()
	for _, r := range rows {
		fmt.Printf("  %-*s  %-*s  %-*s  %s\n", wID, r.ID, wConf, r.Confidence, wEnv, r.Env, r.Console)
	}
	fmt.Print("\n  confidence: official = documented field; undocumented = inferred," +
		" verify once; no-api = provider exposes none\n\n")
	return 0
}

// credentialSpec is where a provider's key comes from: whatever the config
// says, or the provider's conventional environment variable when it says
// nothing. core.Checker derives it the same way; the two must not disagree, or
// `aipocket audit` would name a source the run does not use.
func credentialSpec(p *manifest.Provider, cfg *core.Config) string {
	if spec := cfg.Providers[p.ID].Key; spec != "" {
		return spec
	}
	return "env:" + p.Auth.Env
}

// keySource renders a credential spec for display. LoadConfig has already
// refused anything Parse would reject, so the error branch is unreachable in
// practice — and still must not print the spec, because the reason Parse refuses
// a spec is that it might be a key.
func keySource(spec string) string {
	src, err := secret.Parse(spec)
	if err != nil {
		return "unusable credential spec (not shown)"
	}
	return src.Describe()
}

// audit prints the complete network footprint. Anyone about to hand a tool
// their billing credentials should be able to see, without reading the source,
// exactly which hosts it will talk to and where each key comes from.
//
// "Complete" is doing real work in that sentence, so three things that are easy
// to leave out are listed too: the probe requests, which are not part of a
// normal run but do send the key when you ask for them; an HTTPS_PROXY from the
// environment, which quietly becomes the host that receives every credential;
// and the fact that a `command:` credential helper is a program of the user's
// own choosing, which AIPocket cannot account for.
func audit(reg *manifest.Registry, cfg *core.Config, asJSON bool) int {
	var entries []auditEntry
	var probes []auditEntry
	hosts := map[string]bool{}
	proxies := map[string]bool{}

	for _, p := range reg.All() {
		if cfg.Providers[p.ID].Disabled {
			continue
		}
		src := keySource(credentialSpec(p, cfg))

		record := func(target, host, purpose string, overridden bool) auditEntry {
			proxy := fetch.ProxyFor(target)
			hosts[host] = true
			if proxy != "" {
				proxies[proxy] = true
			}
			return auditEntry{p.ID, host, target, purpose, src, overridden, proxy}
		}

		add := func(ep *manifest.Endpoint, purpose string) {
			if ep == nil {
				return
			}
			// A config override changes the host too. Reporting the manifest's
			// host here would print a host that is never contacted and omit the
			// one that is — under a banner promising the complete footprint.
			target, host, overridden := ep.URL, ep.Host(), false
			if o := cfg.Providers[p.ID].BalanceURL; o != "" && purpose == "balance" {
				if u, err := neturl.Parse(o); err == nil && u.Host != "" {
					target, host, overridden = o, u.Host, true
				}
			}
			entries = append(entries, record(target, host, purpose, overridden))
		}
		add(p.Balance, "balance")
		add(p.Verify, "key check")

		// `aipocket probe` sends the credential to nine speculative paths. It
		// only ever runs when asked, but "the complete footprint" has to include
		// requests the user can trigger, not only the ones a bare run makes.
		if host := probeHost(p); host != "" {
			for _, path := range probePaths {
				probes = append(probes, record("https://"+host+path, host, "probe (only on `aipocket probe "+p.ID+"`)", false))
			}
		}
	}

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{
			"requests":                entries,
			"probe_requests":          probes,
			"hosts":                   sortedKeys(hosts),
			"proxies":                 sortedKeys(proxies),
			"credential_helpers_note": "a command: credential source runs a program of your own choosing, which may itself use the network; it is not part of the list above",
		})
		return 0
	}

	fmt.Print("\n  A normal run makes exactly these requests, and no others:\n\n")
	for _, e := range entries {
		note := ""
		if e.Overridden {
			note = "  [overridden in config]"
		}
		fmt.Printf("    GET %s%s\n        provider=%s  purpose=%s  key from %s\n",
			e.URL, note, e.Provider, e.Purpose, e.KeySource)
	}

	fmt.Printf("\n  hosts contacted: %s\n", strings.Join(sortedKeys(hosts), ", "))
	if len(proxies) > 0 {
		fmt.Printf("  through the proxy set in your environment: %s\n"+
			"      every request above is sent through it, so that host receives the credential too.\n",
			strings.Join(sortedKeys(proxies), ", "))
	}
	fmt.Print("  the registry is compiled into this binary and is never fetched at runtime.\n")

	if len(probes) > 0 {
		fmt.Printf("\n  `aipocket probe <provider>` is not part of a normal run. When you ask for it,\n"+
			"  it sends the key to %d speculative paths on that provider's own host:\n\n", len(probePaths))
		// probes is built in registry order, so a provider's entries are already
		// contiguous; a header when the id changes is all the grouping needed.
		current := ""
		for _, e := range probes {
			if e.Provider != current {
				current = e.Provider
				fmt.Printf("    %s:\n", current)
			}
			fmt.Printf("        GET %s\n", e.URL)
		}
		fmt.Print("\n  `aipocket probe <provider> --dry-run` prints that list and sends nothing.\n")
	}

	fmt.Print("\n  Outside this list: a `command:` credential source runs a program you chose,\n" +
		"  which may use the network itself. AIPocket cannot account for what it does.\n\n")
	return 0
}

// auditEntry is one request `aipocket audit` reports. The JSON tags are a
// contract other tools read, so it lives at package scope rather than inside
// audit.
type auditEntry struct {
	Provider   string `json:"provider"`
	Host       string `json:"host"`
	URL        string `json:"url"`
	Purpose    string `json:"purpose"`
	KeySource  string `json:"key_source"`
	Overridden bool   `json:"overridden_in_config"`
	Proxy      string `json:"proxy,omitempty"`
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// probeePaths are the conventional locations for a balance endpoint. They are
// only ever tried against a host already named in that provider's manifest, so
// probing cannot be pointed at an arbitrary server.
var probePaths = []string{
	"/v1/credits", "/v1/balance", "/v1/quota", "/v1/account", "/v1/usage",
	"/v1/user/balance", "/user/balance", "/v1/billing/balance", "/v1/me",
}

// probeHost is the single host a provider may be probed against: one already
// named in its own manifest.
func probeHost(p *manifest.Provider) string {
	for _, ep := range []*manifest.Endpoint{p.Balance, p.Verify} {
		if ep != nil {
			return ep.Host()
		}
	}
	return ""
}

func probe(ctx context.Context, reg *manifest.Registry, cfg *core.Config,
	id string, timeout time.Duration, dryRun bool) int {

	p, ok := reg.Get(id)
	if !ok {
		fmt.Fprintf(os.Stderr, "aipocket: unknown provider %q\n", id)
		return 2
	}
	// `disabled: true` is a user saying "do not contact this provider". Probing
	// is the most credential-spending thing the tool does — nine requests to
	// paths nobody has documented — so it is the last place to make an exception,
	// and Checker.Selected already refuses a disabled provider named explicitly.
	if cfg.Providers[p.ID].Disabled {
		fmt.Fprintf(os.Stderr, "aipocket: provider %q is disabled in your config\n", id)
		return 2
	}
	host := probeHost(p)
	if host == "" {
		fmt.Fprintf(os.Stderr, "aipocket: %s has no known host to probe\n", id)
		return 2
	}

	if dryRun {
		fmt.Printf("\n  `aipocket probe %s` would send the credential to these %d urls,\n"+
			"  and nothing else:\n\n", p.ID, len(probePaths))
		for _, path := range probePaths {
			line := "https://" + host + path
			if proxy := fetch.ProxyFor(line); proxy != "" {
				line += "  [through " + proxy + "]"
			}
			fmt.Printf("    GET %s\n", line)
		}
		fmt.Printf("\n  key from %s. Nothing was sent.\n\n", keySource(credentialSpec(p, cfg)))
		return 0
	}

	src, err := secret.Parse(credentialSpec(p, cfg))
	if err != nil {
		fmt.Fprintf(os.Stderr, "aipocket: %v\n", err)
		return 2
	}
	key, err := src.Resolve(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aipocket: no credential for %s: %v\n", id, err)
		return 2
	}
	red := &secret.Redactor{}
	red.Add(key)

	client := fetch.New(timeout)
	fmt.Printf("\n  probing https://%s for a balance endpoint (%d paths)\n\n", host, len(probePaths))

	found := 0
	for _, path := range probePaths {
		ep := &manifest.Endpoint{URL: "https://" + host + path}
		// Endpoint.Host is unexported state populated at manifest load; for an
		// ad-hoc probe the URL alone is enough for fetch.Get.
		doc, err := client.Get(ctx, ep, p.Auth, key)
		if err != nil {
			var he *fetch.HTTPError
			if errors.As(err, &he) {
				fmt.Printf("    ✗ %-24s %s\n", path, he.Redacted(red.Apply))
			} else {
				fmt.Printf("    ✗ %-24s %s\n", path, red.Apply(err.Error()))
			}
			continue
		}
		found++
		b, _ := json.Marshal(doc)
		// Redact first, shorten second: a key straddling the cut would survive
		// as a prefix the other way round.
		fmt.Printf("    ✓ %-24s %s\n", path, truncate(red.Apply(string(b)), 220))
	}

	if found == 0 {
		fmt.Printf("\n  Nothing answered. The provider may not expose a balance endpoint,\n" +
			"  or it may sit behind a path this list does not cover.\n\n")
		return 1
	}
	fmt.Printf("\n  If one of those responses holds a balance, add it to providers/%s.yaml\n"+
		"  and open a pull request — that is the whole change:\n\n"+
		"    balance:\n"+
		"      url: https://%s/<path>\n"+
		"      amounts:\n"+
		"        - path: $.<field>\n"+
		"          currency: USD\n\n"+
		"  To use it immediately without waiting for a release, set\n"+
		"  providers.%s.balance_url in your config.\n\n", p.ID, host, p.ID)
	return 0
}

// truncate cuts on a rune boundary; byte slicing would emit broken UTF-8 for a
// provider that answers in a non-ASCII language.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func useColor(disabled bool) bool {
	if disabled || os.Getenv("NO_COLOR") != "" {
		return false
	}
	info, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
