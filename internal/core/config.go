package core

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/naturalmoods/aipocket/internal/manifest"
	"github.com/naturalmoods/aipocket/internal/money"
	"github.com/naturalmoods/aipocket/internal/secret"
)

// Config is the user's file. It contains no secrets — only instructions for
// obtaining them. See internal/secret for the rationale.
type Config struct {
	Providers map[string]ProviderConfig `yaml:"providers"`
}

// ProviderConfig overrides registry defaults for one provider.
type ProviderConfig struct {
	// Key is a credential instruction: env:VAR, command:..., file:...
	// Empty means "use the provider's conventional environment variable".
	Key string `yaml:"key"`

	// BalanceURL overrides the manifest endpoint. This exists for providers
	// whose balance API is undocumented: a user who discovers the real path can
	// use it immediately instead of waiting for a release. Contributing the
	// finding back as a manifest is the better outcome, which is why
	// `aipocket probe` prints the YAML to paste into a pull request.
	BalanceURL string `yaml:"balance_url"`

	// Manual is a user-maintained figure for providers with no balance API at
	// all. It is always reported separately from queried balances and never
	// folded into the verified total, because an unverifiable number carried in
	// the same column as a verified one is how a dashboard starts lying.
	Manual *float64 `yaml:"manual"`

	// AsOf optionally dates a Manual figure: YYYY-MM-DD.
	//
	// Most of the registry publishes no balance API, so a large share of rows are
	// figures somebody typed. Such a number has one real weakness, and it is not
	// precision — it is age. A `manual: 25.00` written eight months ago is not a
	// balance, it is a memory, and nothing in the output said so.
	//
	// It is a string rather than a time.Time on purpose. yaml.v3 decodes an
	// unquoted 2026-08-01 into time.Time but *refuses* the quoted form, and its
	// error is a raw Go time-parse message — which describeYAMLError correctly
	// declines to reproduce, so a user who quoted a perfectly good date would be
	// told only that some value has the wrong type. Parsing it here keeps both
	// forms working and keeps the error message ours.
	AsOf string `yaml:"as_of"`

	// Disabled skips the provider entirely.
	Disabled bool `yaml:"disabled"`
}

// asOfLayout is the only form a manual figure may be dated in. One format, so
// that what the config means never depends on where it is read: a locale-aware
// parser would accept 03/04/2026 as two different days on two machines.
const asOfLayout = "2006-01-02"

func parseAsOf(s string) (time.Time, error) { return time.Parse(asOfLayout, s) }

// today is the current date in UTC. A figure dated "today" in one time zone and
// "tomorrow" in another is not worth modelling; the config states a date, and
// every comparison against it is made in one zone.
func today() time.Time {
	n := time.Now().UTC()
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
}

// yamlLine pulls the one genuinely useful fact out of a yaml.v3 error.
var yamlLine = regexp.MustCompile(`line (\d+)`)

// describeYAMLError renders a decode failure without quoting any of the file.
//
// yaml.v3's messages embed the text at fault: `cannot unmarshal !!str
// `sk-live-01…` into …` for a value, and `field sk-live-01… not found in type
// …` for a key. Stripping only the backquoted part — which is what this used to
// do — misses the second form entirely, so a key pasted in the position of a
// field name still reached stderr and CI logs verbatim. There is no redactor at
// config-load time and there cannot be one: nothing has been resolved yet. So
// the file's contents are not reproduced at all, and what is reported is the
// line number, which is what a person needs to go and look.
func describeYAMLError(err error) string {
	var te *yaml.TypeError
	msgs := []string{err.Error()}
	if errors.As(err, &te) && len(te.Errors) > 0 {
		msgs = te.Errors
	}

	seen := map[string]bool{}
	var problems []string
	for _, m := range msgs {
		where := "somewhere in the file"
		if loc := yamlLine.FindStringSubmatch(m); loc != nil {
			where = "line " + loc[1]
		}
		var what string
		switch {
		case strings.Contains(m, "not found in type"):
			what = "unknown field"
		case strings.Contains(m, "cannot unmarshal"):
			what = "value has the wrong type for this field"
		case strings.Contains(m, "already defined"), strings.Contains(m, "mapping key"):
			what = "duplicate key"
		default:
			what = "syntax error"
		}
		p := where + ": " + what
		if !seen[p] {
			seen[p] = true
			problems = append(problems, p)
		}
	}
	return strings.Join(problems, "; ") +
		" (the text at fault is not reproduced here: no redactor exists yet at " +
		"config-load time, and a mis-pasted credential is the likeliest thing to be wrong)"
}

// ConfigPath returns the config location, honouring XDG_CONFIG_HOME on Unix
// and %AppData% on Windows.
func ConfigPath() (string, error) {
	if p := os.Getenv("AIPOCKET_CONFIG"); p != "" {
		return p, nil
	}
	if runtime.GOOS == "windows" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, "aipocket", "config.yaml"), nil
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "aipocket", "config.yaml"), nil
}

// LoadConfig reads the config file. A missing file is not an error: AIPocket is
// usable with nothing but environment variables, which is what most people
// will try first and what CI will use.
//
// Everything the file says is checked here, at load, rather than when it is
// first used. A credential spec in particular: a spec that Parse would refuse
// used to surface as a per-provider error row, which meant a mistake in one
// line of the config was reported as if a provider had failed.
func LoadConfig(path string) (*Config, error) {
	cfg := &Config{Providers: map[string]ProviderConfig{}}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	// An empty document is not a syntax error. yaml.v3 signals "no document here"
	// with io.EOF, which a file that is empty, holds only a newline, or holds only
	// comments all produce — and `aipocket config path` invites exactly that file
	// into existence, so the obvious first move was being refused with exit code 2
	// and a message about a mis-pasted credential. A missing file was always fine;
	// this is the same case, one byte later.
	if err := dec.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: %s", path, describeYAMLError(err))
	}
	if cfg.Providers == nil {
		cfg.Providers = map[string]ProviderConfig{}
	}
	for _, id := range sortedIDs(cfg.Providers) {
		pc := cfg.Providers[id]
		if pc.Key != "" {
			if _, err := secret.Parse(pc.Key); err != nil {
				return nil, fmt.Errorf("%s: providers.%s.key: %w", path, id, err)
			}
		}
		if pc.BalanceURL != "" {
			u, err := url.Parse(pc.BalanceURL)
			if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
				return nil, fmt.Errorf("%s: providers.%s.balance_url must be a plain https url", path, id)
			}
		}
		// `manual: .nan` is valid YAML and used to be accepted, which made the
		// whole report unencodable — one line in the config blanked out every
		// provider's balance.
		if pc.Manual != nil && !money.Plausible(*pc.Manual) {
			return nil, fmt.Errorf(
				"%s: providers.%s.manual must be a finite amount no larger than %g",
				path, id, money.MaxPlausible)
		}
		if pc.AsOf != "" {
			// A date for a figure that does not exist is a mistake worth
			// reporting, not a line that quietly does nothing: strict both ways
			// is the rule the rest of this file lives by.
			if pc.Manual == nil {
				return nil, fmt.Errorf(
					"%s: providers.%s.as_of dates a manual figure, but no manual: is set",
					path, id)
			}
			d, err := parseAsOf(pc.AsOf)
			if err != nil {
				// The value is not quoted back. It is only meant to be a date,
				// but "meant to be" is exactly the case where echoing it would be
				// the mistake: there is no redactor at config-load time, and a
				// mis-pasted credential can land in any field.
				return nil, fmt.Errorf(
					"%s: providers.%s.as_of must be a date in the form YYYY-MM-DD",
					path, id)
			}
			if d.After(today()) {
				return nil, fmt.Errorf("%s: providers.%s.as_of is in the future", path, id)
			}
		}
	}
	return cfg, nil
}

// ValidateAgainst rejects config entries that name a provider the binary does
// not know.
//
// Silence here is worse than it looks. `openruter: {disabled: true}` is a typo
// that reads as a completed action: the config says the provider is switched
// off, `aipocket audit` says nothing about it either way — and openrouter is
// still contacted on every run, with the key the user believed they had
// withdrawn. The registry is compiled in, so the check costs nothing.
func (c *Config) ValidateAgainst(reg *manifest.Registry) error {
	var unknown []string
	for _, id := range sortedIDs(c.Providers) {
		if _, ok := reg.Get(id); !ok {
			unknown = append(unknown, id)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	subject := "a provider this build does not know"
	if len(unknown) > 1 {
		subject = "providers this build does not know"
	}
	return fmt.Errorf(
		"config names %s: %s — a typo here reads as a setting that took effect, "+
			"while the provider you meant is still being contacted (see `aipocket providers`)",
		subject, strings.Join(unknown, ", "))
}

// sortedIDs keeps every error message and every check deterministic; map
// iteration order would otherwise decide which of two bad entries is reported.
func sortedIDs(m map[string]ProviderConfig) []string {
	out := make([]string, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
