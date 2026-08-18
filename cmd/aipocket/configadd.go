package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/naturalmoods/aipocket/internal/core"
	"github.com/naturalmoods/aipocket/internal/manifest"
	"github.com/naturalmoods/aipocket/internal/secret"
)

// configAdd walks a person through picking a provider and saying where its
// credential comes from, then prints the config entry to paste.
//
// It prints. It does not write the file, and it never asks for the key itself.
// Both restrictions are the design rather than an unfinished first version:
//
//   - AIPocket does not write your config. Loading and re-marshalling that YAML
//     would drop its comments, and the comments are half of what the file is
//     for — they are where the reason for each entry lives.
//   - The config holds instructions for obtaining a credential, never a
//     credential. A subcommand that accepted a key would put it in the shell
//     history and in the process arguments on the way to storing it in the one
//     file the tool promises has no secrets in it.
//
// So this is the ending of `aipocket probe` moved to the front of the job: probe
// prints a manifest block to paste, this prints a config block. What it adds
// over writing the two lines by hand is that the provider id comes from the
// registry and cannot be a typo, and the credential spec is checked by the same
// secret.Parse that LoadConfig will apply — so a spec accepted here is one the
// next run accepts, and a rejected one is refused before it reaches the file.
func configAdd(in io.Reader, out io.Writer, reg *manifest.Registry, cfg *core.Config, path string, args []string) int {
	if len(args) > 1 {
		fmt.Fprint(os.Stderr, "usage: aipocket config add [provider]\n")
		return 2
	}
	r := bufio.NewReader(in)

	p, err := chooseProvider(r, out, reg, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aipocket: %v\n", err)
		return 2
	}
	spec, err := chooseCredential(r, out, p)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aipocket: %v\n", err)
		return 2
	}
	printConfigEntry(out, p, cfg, path, spec)
	return 0
}

// maxAnswers bounds every prompt. `config add` reads a pipe as readily as a
// terminal, and a loop that re-asks forever turns a scripted wrong answer into a
// hang instead of an exit code.
const maxAnswers = 3

// errNoAnswer is what an exhausted or absent stdin becomes. `config add` is
// interactive by design, so nobody to ask is a usage error, not an empty answer
// to act on.
var errNoAnswer = errors.New(
	"`config add` is interactive: it reads the answers from stdin, and there were none")

// ask prints a prompt and reads one line.
func ask(r *bufio.Reader, out io.Writer, prompt string) (string, error) {
	fmt.Fprint(out, prompt)
	line, err := r.ReadString('\n')
	line = strings.TrimSpace(line)
	if err != nil {
		// A final line without a trailing newline is still an answer; anything
		// else at EOF is nobody there.
		if errors.Is(err, io.EOF) && line != "" {
			return line, nil
		}
		fmt.Fprintln(out)
		return "", errNoAnswer
	}
	return line, nil
}

func chooseProvider(r *bufio.Reader, out io.Writer, reg *manifest.Registry, args []string) (*manifest.Provider, error) {
	all := reg.All()
	if len(args) == 1 {
		p, ok := reg.Get(args[0])
		if !ok {
			return nil, fmt.Errorf("unknown provider %q — `aipocket providers` lists them", args[0])
		}
		return p, nil
	}

	width := 0
	for _, p := range all {
		width = maxInt(width, len(p.ID))
	}
	fmt.Fprint(out, "\n  providers in this build:\n\n")
	for i, p := range all {
		fmt.Fprintf(out, "   %2d) %-*s  %s\n", i+1, width, p.ID, p.Name)
	}
	fmt.Fprintln(out)

	for i := 0; i < maxAnswers; i++ {
		ans, err := ask(r, out, "  provider (number or id): ")
		if err != nil {
			return nil, err
		}
		if n, convErr := strconv.Atoi(ans); convErr == nil {
			if n >= 1 && n <= len(all) {
				return all[n-1], nil
			}
		} else if p, ok := reg.Get(strings.ToLower(ans)); ok {
			return p, nil
		}
		// The answer is not echoed back. It is a line someone typed at a prompt
		// in a session about credentials, and a mistyped provider id and a
		// mis-pasted key look identical from here.
		fmt.Fprintf(out, "  not one of the %d providers listed above.\n", len(all))
	}
	return nil, errors.New("no provider chosen")
}

// chooseCredential asks which of the three credential schemes to use and for its
// value, and returns a spec that secret.Parse has accepted.
func chooseCredential(r *bufio.Reader, out io.Writer, p *manifest.Provider) (string, error) {
	fmt.Fprintf(out, "\n  %s — where should AIPocket read the key from, at every run?\n\n"+
		"   1) env      an environment variable (conventional name: %s)\n"+
		"   2) command  ask a secret manager you already have: op, pass, bw, security…\n"+
		"   3) file     a file holding the key, which must be chmod 600\n\n",
		p.ID, p.Auth.Env)

	var scheme, prompt string
	for i := 0; scheme == ""; i++ {
		if i == maxAnswers {
			return "", errors.New("no credential source chosen")
		}
		ans, err := ask(r, out, "  source (1-3): ")
		if err != nil {
			return "", err
		}
		switch ans {
		case "1", "env":
			scheme, prompt = "env", fmt.Sprintf("  variable name [%s]: ", p.Auth.Env)
		case "2", "command":
			scheme, prompt = "command", "  command (e.g. op read op://Private/"+p.ID+"/credential): "
		case "3", "file":
			scheme, prompt = "file", "  path (e.g. ~/.secrets/"+p.ID+"): "
		default:
			fmt.Fprint(out, "  answer 1, 2 or 3.\n")
		}
	}

	for i := 0; i < maxAnswers; i++ {
		value, err := ask(r, out, prompt)
		if err != nil {
			return "", err
		}
		if value == "" && scheme == "env" {
			value = p.Auth.Env
		}
		spec := scheme + ":" + value
		// The same gate LoadConfig applies, so this cannot accept a spec the
		// next run rejects. Its refusals are worded to explain themselves
		// without reproducing what was typed, which is what a prompt that may
		// have just received a pasted key needs.
		if _, err := secret.Parse(spec); err != nil {
			fmt.Fprintf(out, "\n  %v\n\n", err)
			continue
		}
		return spec, nil
	}
	return "", errors.New("no usable credential source")
}

// printConfigEntry prints the block to paste, and then the three things about it
// that are true and easy to get wrong: that nothing was written, that YAML will
// not take a second `providers:` key, and what this provider will and will not
// report once the key resolves.
func printConfigEntry(out io.Writer, p *manifest.Provider, cfg *core.Config, path string, spec string) {
	fmt.Fprintf(out, "\n  Add this to %s:\n\n"+
		"    providers:\n      %s:\n        key: %s\n", path, p.ID, spec)
	if p.Balance == nil {
		// A no-api provider takes the same key — it is checked, not read for a
		// figure — so the entry is only half the answer for one.
		fmt.Fprintf(out, "        # %s publishes no balance endpoint: the key is verified,\n"+
			"        # and any figure comes from you. Optional, never in the verified total:\n"+
			"        manual: 0.00\n        as_of: %s\n",
			p.ID, time.Now().Format("2006-01-02"))
	}
	fmt.Fprint(out, "\n  Nothing was written — AIPocket does not edit your config. If that file\n"+
		"  already has a `providers:` block, put the entry under it: YAML refuses the\n"+
		"  same key twice, and a duplicate is a load error rather than a merge.\n\n")

	fmt.Fprintf(out, "  The key itself is not stored anywhere by AIPocket: it is resolved at every\n"+
		"  run from %s.\n", keySource(spec))
	if spec == "env:"+p.Auth.Env {
		fmt.Fprintf(out, "  That is already the default for %s — with no entry at all, AIPocket reads\n"+
			"  %s. The block is worth keeping only to write the choice down.\n", p.ID, p.Auth.Env)
	}
	if existing := cfg.Providers[p.ID].Key; existing != "" {
		fmt.Fprintf(out, "  Your config already reads %s's key from %s; this replaces that line.\n",
			p.ID, keySource(existing))
	}
	if cfg.Providers[p.ID].Disabled {
		fmt.Fprintf(out, "  Your config also has `disabled: true` for %s, which a key does not\n"+
			"  override: remove it or the provider stays skipped.\n", p.ID)
	}
	fmt.Fprintf(out, "\n  Then: aipocket %s\n  By hand: %s\n\n", p.ID, p.Console)
}
