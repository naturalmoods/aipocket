// Package secret resolves credentials without ever storing them.
//
// The design rule is that AIPocket is not a secret store. Its config file holds
// *instructions for obtaining* a credential, never the credential itself:
//
//	key: env:OPENROUTER_API_KEY
//	key: command:op read op://Private/OpenRouter/credential
//	key: file:~/.secrets/openrouter          # 0600, contents trimmed
//
// This resolves the usual tension between "portable" and "secure". A
// cross-platform keyring library is neither: it pulls in platform-specific
// dependencies, and on WSL — where there is no D-Bus session and no running
// gnome-keyring by default — it simply does not work. Shelling out to whatever
// secret manager the machine already has works everywhere and keeps AIPocket out
// of the business of guarding keys.
//
// Resolved secrets live in memory for the duration of one run and are
// registered with a Redactor so they cannot reach stdout, stderr or logs even
// when a provider echoes the key back in an error body.
package secret

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/naturalmoods/aipocket/internal/safetext"
)

const (
	// commandTimeout bounds an external secret helper. Interactive unlock
	// prompts (Touch ID, master password) need room, but a hung helper must not
	// wedge the whole run.
	commandTimeout = 90 * time.Second

	// killGrace bounds the *second* way a helper can hang. When the deadline
	// fires, os/exec kills the process it started — but `sh -c 'op read … | tee
	// /tmp/x'` can leave a grandchild that still holds the stdout pipe open, and
	// Wait blocks on that pipe, not on the process. WaitDelay gives up on the
	// pipe after this long instead of waiting forever.
	killGrace = 5 * time.Second

	// maxSecretBytes caps a credential source. No API key is 64 KiB, and
	// without a cap `cat /dev/urandom` in a config — or a file that grew by
	// accident — reads until the machine gives out.
	maxSecretBytes = 64 << 10
)

// Redactor scrubs known secret values out of arbitrary text.
//
// This matters more than it sounds: provider error bodies are not trustworthy
// output. During development one provider's 401 response echoed part of the
// submitted key back in the message, which would otherwise have landed in a
// terminal, a CI log, or a bug report.
type Redactor struct {
	mu     sync.RWMutex
	values []string
}

// Add registers a value to be scrubbed. Very short values are ignored: they
// would match innocuous substrings everywhere and turn output into noise.
func (r *Redactor) Add(v string) {
	if len(v) < 8 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.values {
		if existing == v {
			return
		}
	}
	r.values = append(r.values, v)
}

// Apply replaces every registered secret in s with a placeholder.
func (r *Redactor) Apply(s string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, v := range r.values {
		s = strings.ReplaceAll(s, v, "[REDACTED]")
	}
	return s
}

// Source is a parsed credential instruction.
type Source struct {
	Scheme string // env | command | file
	Value  string
}

// envNamePattern is what may be accepted as an environment variable name.
//
// The strictness here is a safety feature, not pedantry, and it is deliberately
// narrower than POSIX. A credential spec is *printed*: `aipocket providers`,
// `aipocket audit`, the JSON output and every MCP tool result name the variable
// a key is read from. So whatever passes this pattern is something the tool
// promises to display — and a key pasted into the config by mistake must never
// be that something.
//
// Lower case is what makes the difference. An earlier version accepted any
// POSIX-shaped name, and real API keys are POSIX-shaped: `gsk_a1b2c3…`,
// `nvapi_…` and anything else built from letters, digits and underscores
// matched, so a mis-pasted key was displayed back as a "variable name" in four
// different outputs with the redactor unable to help — it had never been
// resolved as a secret. Upper case only excludes essentially every real key
// format while still covering every conventional variable name, and the length
// cap keeps a long blob out.
var envNamePattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)

// ValidEnvName reports whether name may be used as a credential's environment
// variable. Provider manifests are held to the same rule as user config, so
// `auth.env` cannot smuggle in a name that Parse would refuse.
func ValidEnvName(name string) bool { return envNamePattern.MatchString(name) }

// ErrLiteralSecret is returned when a credential spec looks like a secret
// rather than an instruction for obtaining one.
var ErrLiteralSecret = errors.New(
	"credential spec must be env:VAR, command:… or file:… — AIPocket does not " +
		"store secrets, and what you supplied looks like a literal value " +
		"(it is not echoed here on purpose)")

// ErrEnvName is returned for an env: spec whose variable name is not one
// AIPocket is willing to print. See envNamePattern.
var ErrEnvName = errors.New(
	"env: must name an environment variable in upper case (A-Z, 0-9 and _, " +
		"at most 64 characters) — the name is printed by `aipocket audit` and " +
		"`aipocket providers`, so a value that could be a key is refused " +
		"(it is not echoed here on purpose)")

// ErrFilePath is returned for a file: spec that is not shaped like a path.
var ErrFilePath = errors.New(
	"file: needs an absolute path or one starting with ~ or ./ — the path is " +
		"printed by `aipocket audit`, so a bare value that could be a key is " +
		"refused (it is not echoed here on purpose)")

// Parse reads a "scheme:value" instruction. The scheme is mandatory: a bare
// string used to be treated as an environment variable name, which turned a
// pasted key into printed output.
func Parse(spec string) (Source, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return Source{}, fmt.Errorf("empty credential spec")
	}
	scheme, value, hasColon := strings.Cut(spec, ":")
	if !hasColon {
		return Source{}, ErrLiteralSecret
	}
	scheme = strings.ToLower(strings.TrimSpace(scheme))
	value = strings.TrimSpace(value)
	switch scheme {
	case "env":
		if !ValidEnvName(value) {
			return Source{}, ErrEnvName
		}
	case "file":
		if !plausiblePath(value) {
			return Source{}, ErrFilePath
		}
	case "command":
		if value == "" {
			return Source{}, fmt.Errorf("credential spec with scheme %q has an empty value", scheme)
		}
	default:
		return Source{}, ErrLiteralSecret
	}
	return Source{Scheme: scheme, Value: value}, nil
}

// plausiblePath keeps a bare secret out of a file: spec. A credential file is
// named by a rooted path in every real config; requiring one costs nothing and
// means `file:sk-live-…` cannot become a printed "path".
//
// The forms are recognised by shape, on every platform, rather than by asking
// filepath.IsAbs. IsAbs answers a different question — "is this absolute *here*"
// — and answers it no for `/etc/keys/foo` on Windows, which wants a drive
// letter, and no for `C:\keys\foo` on Unix. A config file is portable text that
// gets edited on one machine and read on another, so whether a value is shaped
// like a path must not depend on where the check runs. (Windows CI caught this;
// the Linux run was perfectly happy.)
func plausiblePath(v string) bool {
	switch {
	case v == "":
		return false
	case strings.HasPrefix(v, "~"):
		return true // ~/… , expanded later by expandHome
	case strings.HasPrefix(v, "/"), strings.HasPrefix(v, `\`):
		return true // rooted, or a UNC share
	case strings.HasPrefix(v, "./"), strings.HasPrefix(v, `.\`):
		return true // explicitly relative
	default:
		return hasDriveLetter(v)
	}
}

// hasDriveLetter reports whether v begins with a Windows drive prefix such as
// `C:\` or `c:/`. Note that Parse cuts the scheme at the *first* colon, so the
// one in a drive letter survives into the value.
func hasDriveLetter(v string) bool {
	if len(v) < 3 || v[1] != ':' || (v[2] != '\\' && v[2] != '/') {
		return false
	}
	c := v[0] | 0x20 // fold case; only ASCII letters are drive letters
	return c >= 'a' && c <= 'z'
}

// Describe renders the source for display. It is safe to print: for env and
// file it names the location, never the contents. Sanitising is belt and
// braces — env names cannot hold a control character by construction, but a
// path or command line can, and this string is printed to a terminal.
func (s Source) Describe() string {
	switch s.Scheme {
	case "env":
		return "env " + s.Value
	case "file":
		return safetext.Sanitize("file " + s.Value)
	case "command":
		return safetext.Sanitize("command " + firstWord(s.Value))
	}
	return "unknown source"
}

func firstWord(s string) string {
	if i := strings.IndexByte(s, ' '); i > 0 {
		return s[:i]
	}
	return s
}

// ErrNotConfigured means no credential is available for a provider. It is an
// ordinary state — most users configure a handful of the known providers — and
// is reported as "not configured", not as a failure.
type ErrNotConfigured struct{ Detail string }

func (e *ErrNotConfigured) Error() string { return e.Detail }

// Resolve obtains the credential. ctx bounds an external helper, so a hung
// password manager cannot wedge the run — in MCP mode the read loop is single
// threaded and a blocked helper would stall the whole session.
func (s Source) Resolve(ctx context.Context) (string, error) {
	switch s.Scheme {
	case "env":
		v := strings.TrimSpace(os.Getenv(s.Value))
		if v == "" {
			return "", &ErrNotConfigured{Detail: "$" + s.Value + " is not set"}
		}
		return v, nil

	case "file":
		return readFile(s.Value)

	case "command":
		return runCommand(ctx, s.Value)
	}
	return "", fmt.Errorf("unknown credential scheme %q", s.Scheme)
}

func readFile(spec string) (string, error) {
	path, err := expandHome(spec)
	if err != nil {
		return "", err
	}
	// Stat before Open, and check the mode twice. The pre-flight check is not
	// redundant paranoia: opening a FIFO blocks until something writes to it, so
	// `file:/tmp/fifo` would hang the run before any timeout could apply. The
	// check after opening is the authoritative one — it describes the handle
	// actually being read, not a name that may have been swapped underneath.
	if info, err := os.Stat(path); err != nil {
		return "", &ErrNotConfigured{Detail: "cannot read " + spec}
	} else if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", spec)
	}

	f, err := os.Open(path)
	if err != nil {
		return "", &ErrNotConfigured{Detail: "cannot read " + spec}
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", spec)
	}
	// Permission check is advisory on Windows, where Unix mode bits are not
	// meaningful, so it only warns there via the caller.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s is readable by other users (chmod 600 it)", spec)
	}

	b, err := io.ReadAll(io.LimitReader(f, maxSecretBytes+1))
	if err != nil {
		return "", err
	}
	if len(b) > maxSecretBytes {
		return "", fmt.Errorf("%s is larger than %d bytes — that is not a credential", spec, maxSecretBytes)
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		return "", &ErrNotConfigured{Detail: spec + " is empty"}
	}
	return v, nil
}

func runCommand(ctx context.Context, line string) (string, error) {
	// The timeout is enforced by exec.CommandContext rather than by racing a
	// timer against a goroutine. The earlier hand-rolled version read
	// cmd.Process from one goroutine while Start wrote it in another — a data
	// race whose window only opened once the timeout actually fired, so no CI
	// run would ever have caught it — and leaked the goroutine and its pipes.
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()

	// The command runs through the platform shell so configs can use the
	// natural one-liner form documented by password managers. This is not a
	// sandbox boundary: anyone who can edit the config can already run code as
	// you. SECURITY.md states this explicitly.
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", line)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", line)
	}
	cmd.WaitDelay = killGrace

	// Output is captured into a bounded buffer rather than cmd.Output()'s
	// unbounded one, and reading continues past the cap so a helper that keeps
	// writing is not left blocked on a full pipe with nothing draining it.
	out := &capBuffer{max: maxSecretBytes}
	cmd.Stdout = out

	// Stderr is left nil, which sends it to os.DevNull. That is deliberate and it
	// costs real diagnosability: a *failing* credential helper is precisely where
	// a token turns up on stderr — a shell trace from `set -x`, a curl error
	// echoing the request, a vault client printing what it tried — and this error
	// string goes on to a terminal, a CI log and an MCP transcript. No redactor
	// can help: the value was never resolved, so it was never registered. The
	// error below says how to see the real output instead.
	//
	// nil rather than io.Discard for a second reason: os/exec only builds a pipe
	// and a copying goroutine for a writer it does not recognise, and a pipe is
	// something an orphaned grandchild can hold open.
	cmd.Stderr = nil

	err := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "", fmt.Errorf("credential command %q timed out after %s", firstWord(line), commandTimeout)
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if out.over {
		return "", fmt.Errorf("credential command %q produced more than %d bytes — that is not a credential",
			firstWord(line), maxSecretBytes)
	}
	if err != nil {
		return "", fmt.Errorf(
			"credential command %q failed (%s); its output is deliberately not shown, "+
				"because a failing helper is the most likely place for a token to appear "+
				"on stderr — run the command yourself to see why",
			firstWord(line), exitDescription(err))
	}
	v := strings.TrimSpace(out.String())
	if v == "" {
		return "", &ErrNotConfigured{Detail: "credential command produced no output"}
	}
	return v, nil
}

// exitDescription names the failure without quoting anything the helper wrote.
func exitDescription(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return fmt.Sprintf("exit status %d", ee.ExitCode())
	}
	var pe *os.PathError
	if errors.As(err, &pe) {
		return "the shell could not be started"
	}
	return "could not be run"
}

// capBuffer accumulates up to max bytes and then keeps accepting writes without
// storing them, so the writer never blocks and the reader never grows.
type capBuffer struct {
	max  int
	b    []byte
	over bool
}

func (c *capBuffer) Write(p []byte) (int, error) {
	if room := c.max - len(c.b); room > 0 {
		if len(p) <= room {
			c.b = append(c.b, p...)
		} else {
			c.b = append(c.b, p[:room]...)
			c.over = true
		}
	} else if len(p) > 0 {
		c.over = true
	}
	return len(p), nil
}

func (c *capBuffer) String() string { return string(c.b) }

func expandHome(p string) (string, error) {
	if !strings.HasPrefix(p, "~") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~")), nil
}
