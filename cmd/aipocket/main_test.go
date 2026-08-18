package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Exit codes are one of the four things v1.0.0 promises not to change: 0
// success, 1 at least one provider errored, 2 usage error. Scripts branch on
// them, and until now nothing checked them — run() could start returning 1 for a
// usage error and the whole suite would stay green.
//
// run() is deliberately separate from main() so it can be called here; main()
// does nothing but pass its result to os.Exit.

// runArgs calls run() with argv, capturing everything it prints.
//
// Every caller must have set AIPOCKET_CONFIG first. Without it these tests read
// the developer's own config file — and through it their own credentials, and
// through those the real providers. A test suite must not be able to spend money
// or leak a key by accident, so hermeticity here is not tidiness.
func runArgs(t *testing.T, argv ...string) (code int, output string) {
	t.Helper()
	if os.Getenv("AIPOCKET_CONFIG") == "" {
		t.Fatal("set AIPOCKET_CONFIG before calling runArgs")
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	captured := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		captured <- string(b)
	}()

	oldArgs, oldOut, oldErr := os.Args, os.Stdout, os.Stderr
	os.Args = append([]string{"aipocket"}, argv...)
	os.Stdout, os.Stderr = w, w
	code = run()
	os.Args, os.Stdout, os.Stderr = oldArgs, oldOut, oldErr

	w.Close()
	return code, <-captured
}

func writeConfig(t *testing.T, body string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AIPOCKET_CONFIG", path)
}

func TestExitCodeZeroForSuccessAndForNothingToDo(t *testing.T) {
	writeConfig(t, "providers: {}\n")

	if code, out := runArgs(t, "version"); code != 0 {
		t.Errorf("version exited %d, want 0:\n%s", code, out)
	}
	if code, out := runArgs(t, "config", "path"); code != 0 {
		t.Errorf("config path exited %d, want 0:\n%s", code, out)
	}

	// A missing credential is StateUnconfigured, which the tool deliberately does
	// not treat as a failure. If this ever returns 1, every CI job that runs
	// aipocket without secrets starts failing for a non-problem.
	t.Setenv("DEEPSEEK_API_KEY", "")
	if code, out := runArgs(t, "deepseek"); code != 0 {
		t.Errorf("an unconfigured provider exited %d, want 0:\n%s", code, out)
	}
}

func TestExitCodeTwoForUsageErrors(t *testing.T) {
	writeConfig(t, "providers: {}\n")

	if code, out := runArgs(t, "nosuchprovider"); code != 2 {
		t.Errorf("an unknown provider exited %d, want 2:\n%s", code, out)
	}
	if code, out := runArgs(t, "config", "wrong"); code != 2 {
		t.Errorf("a malformed subcommand exited %d, want 2:\n%s", code, out)
	}
	// --dry-run exists to stop the tool from sending a credential. Ignoring it
	// where it does not apply would be the one silent failure that matters, so it
	// is a usage error instead.
	if code, out := runArgs(t, "--dry-run"); code != 2 {
		t.Errorf("--dry-run outside probe exited %d, want 2:\n%s", code, out)
	}
	if !strings.Contains(mustRun(t, "--dry-run"), "aipocket probe") {
		t.Error("the --dry-run refusal should say where the flag does apply")
	}
}

// A config naming a provider this build does not know is a usage error too: the
// typo reads as a setting that took effect while the real provider keeps being
// contacted.
func TestExitCodeTwoForAConfigNamingAnUnknownProvider(t *testing.T) {
	writeConfig(t, "providers:\n  openruter:\n    disabled: true\n")

	code, out := runArgs(t, "version")
	if code != 2 {
		t.Errorf("an unknown provider id in the config exited %d, want 2:\n%s", code, out)
	}
	if strings.Contains(out, "openruter: disabled") {
		t.Error("the error must not reproduce the config file")
	}
}

// 1 means "at least one provider errored" — the code a script checks before
// trusting the numbers it just read.
func TestExitCodeOneWhenAProviderFails(t *testing.T) {
	// Port 1 on the loopback interface: refused immediately, no DNS lookup, and
	// nothing leaves the machine. The point is a transport failure, not a
	// pretend endpoint.
	writeConfig(t, "providers:\n  deepseek:\n    balance_url: https://127.0.0.1:1/v1/balance\n")
	t.Setenv("DEEPSEEK_API_KEY", "sk-test-abcdefghijklmnopqrst")
	// An HTTPS_PROXY in the developer's environment would send that key to a
	// third host instead of failing. http.ProxyFromEnvironment reads the
	// environment once per process, and this is the only test here that makes a
	// request, so clearing it now is enough.
	for _, v := range []string{"HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy"} {
		t.Setenv(v, "")
	}

	code, out := runArgs(t, "deepseek", "--no-color", "--timeout", "3s")
	if code != 1 {
		t.Errorf("a failed provider exited %d, want 1:\n%s", code, out)
	}
	if strings.Contains(out, "sk-test-abcdefghijklmnopqrst") {
		t.Error("the credential reached the output")
	}
}

func mustRun(t *testing.T, argv ...string) string {
	t.Helper()
	_, out := runArgs(t, argv...)
	return out
}
