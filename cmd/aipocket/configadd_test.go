package main

import (
	"os"
	"strings"
	"testing"

	"github.com/naturalmoods/aipocket"
)

// runStdin is runArgs with answers to read. `config add` is the only subcommand
// that reads stdin, so the plumbing lives here rather than in runArgs, whose
// callers must keep getting a closed-off stdin.
func runStdin(t *testing.T, answers string, argv ...string) (code int, output string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(answers); err != nil {
		t.Fatal(err)
	}
	w.Close() // EOF after the last answer: an unanswered prompt must not hang

	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old; r.Close() }()

	return runArgs(t, argv...)
}

// The whole point of `config add` is that it produces text and changes nothing.
// It exists because a provider id typed by hand can be a typo the tool would
// then refuse, not because AIPocket wants to own that file — writing it would
// drop the comments that explain each entry, which is a documented non-goal.
func TestConfigAddPrintsAnEntryToPasteAndWritesNothing(t *testing.T) {
	const body = "# a comment worth keeping\nproviders:\n  fal:\n    key: env:FAL_KEY\n"
	writeConfig(t, body)
	path := os.Getenv("AIPOCKET_CONFIG")

	code, out := runStdin(t, "1\nDEEPSEEK_TOKEN\n", "config", "add", "deepseek")
	if code != 0 {
		t.Fatalf("config add exited %d, want 0:\n%s", code, out)
	}
	for _, want := range []string{"providers:", "deepseek:", "key: env:DEEPSEEK_TOKEN", path} {
		if !strings.Contains(out, want) {
			t.Errorf("the printed entry is missing %q:\n%s", want, out)
		}
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != body {
		t.Errorf("config add modified the config file:\n%s", after)
	}
}

// Choosing from the registry is the reason to run this at all: the number and
// the id must reach the same provider, and neither can be an id the tool would
// later reject.
func TestConfigAddPicksTheProviderTheRegistryLists(t *testing.T) {
	writeConfig(t, "providers: {}\n")
	reg, err := aipocket.Registry()
	if err != nil {
		t.Fatal(err)
	}
	first := reg.All()[0]

	_, out := runStdin(t, "1\n1\n\n", "config", "add")
	if !strings.Contains(out, "      "+first.ID+":\n") {
		t.Errorf("choosing provider 1 did not print an entry for %s:\n%s", first.ID, out)
	}
	// An empty answer at the variable prompt means the conventional name, which
	// is what `aipocket providers` already advertises for that provider.
	if !strings.Contains(out, "key: env:"+first.Auth.Env) {
		t.Errorf("the default variable name for %s is missing:\n%s", first.ID, out)
	}

	code, out := runStdin(t, "1\nX\n", "config", "add", "openruter")
	if code != 2 {
		t.Errorf("an unknown provider exited %d, want 2:\n%s", code, out)
	}
}

// The prompt asking where a key comes from is exactly where somebody pastes the
// key instead. secret.Parse refuses it — every real key format is lower case,
// while every conventional variable name is not — and the refusal must not print
// what was pasted: this output goes to a terminal, into scrollback, and into
// whatever the session is being recorded by.
func TestConfigAddRefusesAPastedKeyWithoutEchoingIt(t *testing.T) {
	writeConfig(t, "providers: {}\n")
	const key = "sk-a1b2c3d4e5f6g7h8i9j0"

	code, out := runStdin(t, "1\n"+key+"\n", "config", "add", "deepseek")
	if code != 2 {
		t.Errorf("a pasted key exited %d, want 2:\n%s", code, out)
	}
	if strings.Contains(out, key) {
		t.Errorf("the pasted key was echoed back:\n%s", out)
	}
	if !strings.Contains(out, "upper case") {
		t.Errorf("the refusal should say what env: accepts:\n%s", out)
	}
	// And it must not have printed a block for the user to paste anyway.
	if strings.Contains(out, "key: env:") {
		t.Errorf("config add printed an entry after refusing the spec:\n%s", out)
	}
}

// Nobody to ask is a usage error. `config add` reads a pipe as readily as a
// terminal, so an exhausted stdin has to end the run rather than loop on a
// prompt nothing will ever answer.
func TestConfigAddWithNoAnswersIsAUsageErrorNotAHang(t *testing.T) {
	writeConfig(t, "providers: {}\n")

	code, out := runStdin(t, "", "config", "add", "deepseek")
	if code != 2 {
		t.Errorf("config add with no input exited %d, want 2:\n%s", code, out)
	}
	if !strings.Contains(out, "interactive") {
		t.Errorf("the error should say the command needs answers:\n%s", out)
	}
}
