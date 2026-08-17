package secret

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseSchemes(t *testing.T) {
	cases := map[string]Source{
		"env:FOO":             {Scheme: "env", Value: "FOO"},
		"command:op read x":   {Scheme: "command", Value: "op read x"},
		"file:~/.secrets/foo": {Scheme: "file", Value: "~/.secrets/foo"},
		"file:/etc/keys/foo":  {Scheme: "file", Value: "/etc/keys/foo"},
		"  env:FOO  ":         {Scheme: "env", Value: "FOO"},
	}
	for in, want := range cases {
		got, err := Parse(in)
		if err != nil {
			t.Errorf("Parse(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("Parse(%q) = %+v, want %+v", in, got, want)
		}
	}
	if _, err := Parse(""); err == nil {
		t.Error("empty spec should fail")
	}
}

// A user pasting their actual key into the config is a likely mistake. It must
// be refused, and the value must not appear in the error — an earlier version
// treated it as an environment variable *name* and printed it in `aipocket
// providers`, `aipocket audit`, the JSON output and MCP tool results.
func TestLiteralKeyInConfigIsRefusedAndNotEchoed(t *testing.T) {
	for _, spec := range []string{
		"sk-live-0123456789abcdef",
		"sk-or-v1-abc123",
		"op://Private/OpenRouter/credential",
		"nope:sk-live-0123456789abcdef",
		"env:sk-live-0123456789",
	} {
		src, err := Parse(spec)
		if err == nil {
			t.Errorf("Parse(%q) was accepted as %+v; want rejection", spec, src)
			continue
		}
		if strings.Contains(err.Error(), "sk-live") || strings.Contains(err.Error(), "sk-or") {
			t.Errorf("Parse(%q) echoed the value: %v", spec, err)
		}
	}
}

// The leak that survived the first fix. These are real key *formats*, and every
// one of them is a syntactically valid POSIX environment variable name, so a
// pattern that only asked for "letters, digits and underscores" let them
// through and Describe printed them straight back out.
func TestKeyShapedEnvNamesAreRefusedAndNotEchoed(t *testing.T) {
	for _, name := range []string{
		"gsk_A1b2C3d4E5f6G7h8I9j0",     // Groq
		"nvapi_7f3c9d2e1a8b4c6d5e0f",   // NVIDIA
		"r8_Hq3TnZ9pLm2xVb7Kd4Ws1Yc6",  // Replicate
		"hf_QwErTyUiOpAsDfGhJkLzXcVbN", // Hugging Face
		"AIzaSyD9x2Kq7Lm4Np8Rt3Vw6Yz1", // Google
		"sk_live_51H8xKq2Lm9Np4Rt7Vw",  // lower-case run in an otherwise loud name
	} {
		for _, spec := range []string{name, "env:" + name} {
			src, err := Parse(spec)
			if err == nil {
				t.Errorf("Parse(%q) accepted a key-shaped name as %+v", spec, src)
				continue
			}
			if strings.Contains(err.Error(), name) {
				t.Errorf("Parse(%q) echoed the value: %v", spec, err)
			}
		}
	}
}

// A bare string is no longer an environment variable name. The scheme is what
// distinguishes "read this variable" from "here is my key", and guessing was
// the whole source of the leak above.
func TestBareStringIsNotTreatedAsAnEnvName(t *testing.T) {
	if src, err := Parse("OPENROUTER_API_KEY"); err == nil {
		t.Fatalf("a bare name was accepted as %+v; env: is required", src)
	}
	if _, err := Parse("env:OPENROUTER_API_KEY"); err != nil {
		t.Fatalf("the explicit form must still work: %v", err)
	}
}

// file: values are printed by `aipocket audit` too, so the same rule applies:
// something that is not shaped like a path is not accepted as one.
func TestBareValueIsNotTreatedAsAFilePath(t *testing.T) {
	const key = "sk-live-0123456789abcdef"
	src, err := Parse("file:" + key)
	if err == nil {
		t.Fatalf("file:%s was accepted as %+v", key, src)
	}
	if strings.Contains(err.Error(), "sk-live") {
		t.Fatalf("the value was echoed: %v", err)
	}
}

// Describe is printed in `aipocket providers` and `aipocket audit`. It must name
// the location without ever revealing the value.
func TestDescribeNeverLeaksAValue(t *testing.T) {
	t.Setenv("SECRET_ENV", "sk-live-do-not-print")
	s, _ := Parse("env:SECRET_ENV")
	if d := s.Describe(); strings.Contains(d, "sk-live") {
		t.Fatalf("Describe leaked the value: %q", d)
	}
}

// Describe reaches a terminal, and a path or command line is free-form text.
func TestDescribeStripsTerminalControlSequences(t *testing.T) {
	s := Source{Scheme: "command", Value: "op\x1b[2Jread"}
	if d := s.Describe(); strings.Contains(d, "\x1b") {
		t.Fatalf("an escape sequence survived Describe: %q", d)
	}
}

func TestResolveEnv(t *testing.T) {
	t.Setenv("TEST_KEY", "  sk-value-here  ")
	s, _ := Parse("env:TEST_KEY")
	got, err := s.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "sk-value-here" {
		t.Fatalf("got %q", got)
	}
}

// "Not set" is an ordinary state, distinguishable from a real failure so the
// caller can report "unconfigured" instead of "error".
func TestUnsetEnvIsTypedAsNotConfigured(t *testing.T) {
	t.Setenv("TEST_KEY", "")
	s, _ := Parse("env:TEST_KEY")
	_, err := s.Resolve(context.Background())
	var nc *ErrNotConfigured
	if !errors.As(err, &nc) {
		t.Fatalf("got %T (%v), want *ErrNotConfigured", err, err)
	}
}

func TestFileMustNotBeWorldReadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits are not meaningful on windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "key")
	if err := os.WriteFile(path, []byte("sk-value-here"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := Parse("file:" + path)
	if _, err := s.Resolve(context.Background()); err == nil {
		t.Fatal("a 0644 credential file should be refused")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := s.Resolve(context.Background())
	if err != nil || got != "sk-value-here" {
		t.Fatalf("got %q, %v", got, err)
	}
}

// Only a regular file is read. A directory is the portable case; the unix build
// also covers a FIFO, where an unguarded Open blocks until someone writes.
func TestFileSourceRefusesSomethingThatIsNotAFile(t *testing.T) {
	s, err := Parse("file:" + t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resolve(context.Background()); err == nil {
		t.Fatal("a directory was accepted as a credential file")
	}
}

// Without a cap this reads until the machine gives out.
func TestOversizedCredentialFileIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maxSecretBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	s, _ := Parse("file:" + path)
	if _, err := s.Resolve(context.Background()); err == nil {
		t.Fatalf("a %d-byte file was accepted as a credential", maxSecretBytes+1)
	}
}

func TestResolveCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell differs on windows; covered by the unix path")
	}
	s, _ := Parse("command:printf 'sk-from-manager\\n'")
	got, err := s.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "sk-from-manager" {
		t.Fatalf("got %q", got)
	}
}

// A failing helper is the most likely place for a token to appear on stderr: a
// shell trace, a curl error echoing the request, a vault client reporting what
// it tried. That text would go on to a terminal, a CI log and an MCP
// transcript, and no redactor can help — the value was never resolved, so it
// was never registered. The helper's name and exit status are enough.
func TestFailingCommandDoesNotSurfaceItsStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell differs on windows")
	}
	s, _ := Parse("command:echo 'token sk-live-0123456789abcdef leaked' >&2; exit 3")
	_, err := s.Resolve(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "sk-live") || strings.Contains(err.Error(), "leaked") {
		t.Fatalf("the helper's stderr reached the error: %v", err)
	}
	if !strings.Contains(err.Error(), "exit status 3") {
		t.Errorf("the exit status is the useful part and is missing: %v", err)
	}
}

func TestOversizedHelperOutputIsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell differs on windows")
	}
	s, _ := Parse("command:head -c 200000 /dev/zero | tr '\\0' 'x'")
	if _, err := s.Resolve(context.Background()); err == nil {
		t.Fatal("an unbounded helper output was accepted as a credential")
	}
}

// A helper that never returns must not wedge the run. The deadline kills the
// process; WaitDelay is what stops a grandchild holding the pipe open from
// blocking Wait long after that.
func TestHangingHelperIsBoundedByTheContext(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell differs on windows")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	s, _ := Parse("command:sleep 30 | cat")
	done := make(chan error, 1)
	go func() { _, err := s.Resolve(ctx); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected the deadline to be reported")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Resolve did not return after its context expired")
	}
}

func TestRedactor(t *testing.T) {
	r := &Redactor{}
	r.Add("sk-live-abcdefgh")
	r.Add("short") // too short to register; would match everywhere
	got := r.Apply("failed for key sk-live-abcdefgh in short order")
	if strings.Contains(got, "sk-live-abcdefgh") {
		t.Fatalf("secret survived: %q", got)
	}
	if !strings.Contains(got, "short order") {
		t.Fatalf("over-eager redaction mangled the text: %q", got)
	}
}

func TestRedactorIsIdempotentAndConcurrencySafe(t *testing.T) {
	r := &Redactor{}
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			r.Add("sk-live-abcdefgh")
			_ = r.Apply("sk-live-abcdefgh")
			close2(done)
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}

// close2 sends rather than closes so the loop above can drain a fixed count.
func close2(ch chan struct{}) { ch <- struct{}{} }
