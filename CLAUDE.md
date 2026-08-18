# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

**AIPocket** — a single static Go binary that reports remaining prepaid credit at LLM API
providers. Module path `github.com/naturalmoods/aipocket`; the binary and all
user-facing commands are lowercase `aipocket`, built from `./cmd/aipocket`.

Naming convention, applied throughout: **AIPocket** is the product (prose, docs, printed
sentences about what the tool does), `aipocket` is everything a machine reads — module path,
package names, binary, command examples, error prefixes, config directory, MCP server name,
User-Agent. Keep that split when adding code or docs. The checkout directory happens to be
`AIPocket`, which is irrelevant to Go; the module path is authoritative and lowercase.

## Open work

Planned work lives in GitHub issues under the **v1.0.0** milestone:
<https://github.com/naturalmoods/aipocket/milestone/1>. Each one is written to stand on
its own — which file, which invariant, what the test has to prove — because the reasoning
behind a change is exactly what a diff does not preserve. #5 and #6 are provider
checklists rather than single tasks; the rest are roughly one sitting each. Each issue names
the test that closes it — see **Testing conventions**, which is the gate, not a preference.

**Non-goals** below is the other half of that record: what was decided against, and why.
A plausible-sounding feature that appears in neither list is worth asking about before
building it.

## Commands

```bash
go build ./cmd/aipocket    # build; ./aipocket and *.exe are gitignored
go test ./...              # full suite (CI also runs -race)
go test -race ./...
go vet ./...
go test ./internal/core -run TestNegativeDerivedBalanceIsAnError   # one test
go test ./internal/mcp -run TestBatch -v                           # one prefix, verbose
go mod tidy -diff          # CI fails if this reports a diff
```

Running against the real config is often not what you want while developing; point it
elsewhere with `AIPOCKET_CONFIG=/path/to/config.yaml` or `--config PATH`.

```bash
go run ./cmd/aipocket audit      # no network, no credentials needed
go run ./cmd/aipocket providers
AIPOCKET_CONFIG=/tmp/c.yaml go run ./cmd/aipocket --json
```

Exit codes: `0` success, `1` at least one provider errored, `2` usage error.

Version is injected at build time into **two** places — `main.version` and
`github.com/naturalmoods/aipocket/internal/fetch.Version` (the User-Agent). See `.goreleaser.yaml`;
changing one without the other ships a binary that misreports itself. Both default to
`""`, not `"dev"`: `fetch.ResolvedVersion` falls back to `debug.ReadBuildInfo`, so a
`go install …@latest` binary still names its release rather than calling itself "dev",
which is the one question worth answering after an advisory.

`go.mod`'s `go` directive is the release compiler (`release.yml` uses
`go-version-file: go.mod`) and is kept on a supported patch release; `ci.yml` floats on
`stable` to catch breakage early.

## Architecture

Providers are **data**, the engine is code. One YAML file in `providers/` adds a provider;
no Go changes. That split is the reason the codebase is shaped the way it is.

Flow of one run:

```
providers/*.yaml  ──embed──>  registry.go  ──>  manifest.Load (validate + compile jpath)
                                                      │
config.yaml ──> core.LoadConfig ─────────────────────>│
                                                      v
                          core.Checker.Run  ──>  secret.Source.Resolve (env/command/file)
                                                 fetch.Client.Get (authenticated GET)
                                                 manifest.Amount.Resolve (jpath)
                                                      v
                                            []core.Result / core.Report
                                                      v
                            render.Table / render.JSON / internal/mcp tool results
```

- `registry.go` (package `aipocket`, repo root) — `//go:embed all:providers`. The only public
  API surface.
- `internal/manifest` — the declarative provider format, plus **all validation**, done at
  load time. A malformed manifest is a failing test, never a runtime surprise.
- `internal/core` — `Checker` (concurrent, one goroutine per provider) produces
  `Result`/`Report`. Every output surface renders the same `Result` slice, so table, JSON
  and MCP cannot drift apart in what they claim.
- `internal/jpath` — the deliberately tiny JSONPath subset: `$.a.b`, `$.a[0]`,
  `$.a[?k=v]`, and combinations. Nothing else. Extending it needs a discussion first.
- `internal/secret` — credential *instructions* (`env:`/`command:`/`file:`) and the
  `Redactor`.
- `internal/fetch` — the single network operation; restrictive by design.
- `internal/mcp` — hand-written MCP server over stdio (no SDK).
- `internal/render` — table and JSON.
- `internal/money` — `Plausible`, the one rule for what may be an amount. Every
  producer of a figure calls it: jpath on parse, manifest on a `total - used`
  difference, `LoadConfig` on `manual:`, `Checker.Run` before totalling.
- `internal/safetext` — `Sanitize`, applied to anything untrusted that reaches a
  terminal. Called by `fetch` (provider error bodies), `secret` (`Describe`) and
  `render` (every cell).

## Invariants

These are not style preferences; each has a test that fails loudly if removed. Breaking one
silently is the failure mode the project exists to prevent.

**Three totals, never one.** `TotalVerified` contains only USD balances read from a
*documented* field (`status: official`). Inferred (`undocumented`) and user-typed (`manual`)
figures are totalled separately and listed in `Excluded`. Non-USD balances are reported but
never converted — there is no exchange-rate logic anywhere and there should not be.

**Refuse to be confidently wrong.** A response matching no amount, a derived
`total - used` that comes out negative, a `NaN`/`Inf`/hex-float amount (`strconv` accepts
all three) — each becomes a visible `StateError`, never a green `0.00`. A missing
credential is `StateUnconfigured`, which is not an error. Every figure passes
`money.Plausible` where it is *produced*, not only where it is read: two operands
inside the bound can subtract to one outside it, and `manual: .nan` is valid YAML.

**Redact before truncating, sanitize after both.** `Redactor.Apply` runs over the
*whole* text before any shortening (`fetch.HTTPError.RedactedBody`,
`core.Checker.describe`, `probe` in `main.go`). Truncate-first lets a key straddling the
cut survive as a prefix; a real provider echoes part of the submitted key in its 401
body. There is a dedicated boundary test, and it inspects the whole marshalled `Result`,
not one field. `safetext.Sanitize` runs *after* redaction — rewriting the text first
could split a secret across the substitution.

**The provider's words and the tool's are separate fields.** `Result.Error` is
AIPocket's own account of a failure; `Result.ProviderMessage` is untrusted text the
provider chose. The table prints both (the provider's in quotes), the MCP server
forwards only the first. `HTTPError.Error()` omits the body altogether, so an
ordinary `%v` log cannot leak one. Concatenating them back together would undo all
three protections at once.

**The config file holds no secrets, and errors about it echo nothing.**
`secret.Parse` requires a scheme, an upper-case `env:` name and a path-shaped `file:`
value — every real key format is a valid POSIX variable name, so a looser rule printed
mis-pasted keys back out. Specs are validated at *load* time, not per row.
`core.describeYAMLError` reports a line number and a category and reproduces none of
the file: yaml.v3 embeds the offending text in both the value position *and* the
field-name position, and no redactor exists yet at config-load time.

**A config that names an unknown provider is an error.** `Config.ValidateAgainst`
runs in `main` before anything else. `openruter: {disabled: true}` is a typo that
reads as a completed action while the real provider keeps being contacted.

**The registry is never fetched at runtime.** A manifest names the host an API key is sent
to; a network-updatable registry would be a remote "send your credentials here" primitive.
New providers ship in a new release.

**Transport rules are not the caller's to opt out of.** https only, redirects refused
(a 3xx is an error), 1 MiB body cap, deadline on every request. `fetch.NewWithTransport`
exists for tests and proxies but still applies the redirect and timeout policy.

**`aipocket audit` must be exhaustive and exact.** It prints every request that would be made.
Config overrides (`balance_url`) are reflected there, disabled providers omitted — and
because audit omits them, `Checker.Selected` *and* `probe` refuse an explicitly named
disabled provider rather than quietly contacting it. Any new network call must appear in
audit. So must the requests only a subcommand makes: the nine `probe` paths are listed
under their own heading (`probe --dry-run` prints them and sends nothing), an
`HTTPS_PROXY` from the environment is named per request via `fetch.ProxyFor` because it
becomes another host that receives the key, and the closing paragraph says plainly that a
`command:` helper is a program AIPocket cannot account for.

**Strict YAML both ways.** `dec.KnownFields(true)` on manifests and on user config: a typo
must fail, not silently no-op. `as_of:` without a `manual:` is an error for the same
reason — a date for a figure that does not exist is a mistake, not a no-op.

**A hand-kept figure states its age, and nothing more.** `as_of:` renders as
"user-maintained figure (2026-08-01, 17 days ago)". There is deliberately no staleness
threshold and no warning: how old is too old depends on the spend rate, and a tool that
keeps no history cannot know it. `AsOf` is a `string` parsed by `core.parseAsOf`, not a
`time.Time`, because yaml.v3 refuses the *quoted* form of a date and its error is a raw
Go time-parse message that `describeYAMLError` correctly declines to reproduce — a user
who quoted a good date would be told only that some value has the wrong type.

## Non-goals

Decisions, not gaps. Each of these is an obvious, helpful-sounding addition that would
change what the tool is, so implementing one needs a conversation first rather than a pull
request.

- **No currency conversion.** A non-USD balance is reported and excluded from the verified
  total. There is no exchange rate in the binary, and one fetched at runtime would be a
  second network dependency deciding what a number means.
- **No spend history, usage tracking, cache or database.** AIPocket answers "how much is
  left, right now". Keeping state is what turns an auditable static binary into a service.
- **No runway estimate** ("enough for 12 days"). It needs a spend rate, which needs
  history, which is the previous item.
- **No TUI, no web UI, no alerts.** The table is the interface; `--json` is the
  integration point.
- **No keyring library.** `command:` delegates to whatever secret manager the machine
  already has, and works where a keyring dependency does not — WSL has no D-Bus session
  and no keyring daemon.
- **AIPocket does not write your config.** No `aipocket set groq 25.00`. Rewriting the
  file would lose its comments, and its whole purpose is to hold credential instructions.
- **GET only.** `fetch` performs one operation. A provider reachable only by POST is not
  added; that restriction is what makes `aipocket audit`'s promise checkable.
- **No remote registry.** An invariant above, repeated here because "just fetch the
  provider list" is the most natural feature request this design has to refuse.
- **Non-money quotas** (a character allowance, a monthly request cap) need no code:
  `currency:` is a free string, so `currency: CHARS` would already be reported and already
  kept out of the USD total. Whether a tool about money should carry them is a product
  question, and the answer for 1.0 is no.

## Adding or changing a provider

Write `providers/<id>.yaml` and run `go test ./...`. `aipocket probe <id>` searches
conventional paths — only against a host already named in that provider's manifest — and
prints a manifest block to paste.

Status choice is enforced by `registry_test.go`:

- `official` — provider documents endpoint **and** field; requires `docs:`. Only this
  status enters the verified total.
- `undocumented` — response shape inferred; requires `notes:` saying what was guessed.
- `no-api` — no balance endpoint; must **not** define `balance:`, use `verify:` instead,
  requires `notes:`.

`auth.env` must be an upper-case name (`secret.ValidEnvName`, enforced in
`manifest.validate`): the manifest's variable name becomes a credential spec and is
printed, so it is held to the same rule as user config.

`auth.scheme` is the word before the credential in an `Authorization` header, `Bearer`
unless a manifest says otherwise (fal documents `Key`). Validated as a single
`[A-Za-z][A-Za-z0-9-]*` token, and refused outright with `type: header`, where the
credential is written raw and a scheme would silently mean nothing.

`auth.headers` carries static, non-secret headers a provider requires (Anthropic's
`anthropic-version`). Literal values only — a secret there would be a second
credential the redactor and `audit` know nothing about. Validation refuses a name
outside `[A-Za-z0-9-]+`, a value that is not non-empty printable ASCII, and the
four names the tool sets itself (`Authorization`, `Accept`, `User-Agent`, and
`auth.header`). `fetch.Get` applies them *before* the credential, so even a bug in
that validation cannot displace the key; `audit` prints them with their request.

Every provider needs `console:` so a human can check by hand. Marking an inferred reading
`official` moves a guess into the number users trust — that is the one change to reject
outright.

`amounts` is an ordered candidate list; the first that resolves wins. Either `path:` or the
`total:`/`used:` pair, plus `currency:`. Only derived (`total - used`) amounts get the
negative check — a direct field could legitimately be negative for an overdraft model.

`scale:` converts a provider's unit into the currency's own (Novita reports 1/10000 USD).
It is a `*float64` so `scale: 0` is distinguishable from no scale and can be refused;
`money.Plausible` runs on the *scaled* figure, since that is the one reported. Note that
the plausibility bound is no defence against a missing scale — an unconverted 1e6 is a
believable number of dollars — which is why the registry test resolves every balance
manifest against the provider's own documented example.

## MCP server

`aipocket mcp` speaks newline-delimited JSON-RPC on stdio. Two read-only tools:
`get_balances`, `list_providers`. Protocol details that are easy to break:

- stdout carries protocol traffic **only**; diagnostics go to stderr. A stray log line
  corrupts the stream.
- A notification (no `id`) is never answered — including a *malformed* one, checked before
  envelope validation. Replying shifts every subsequent response for an in-order client.
- JSON-RPC batches are supported (required by pre-2025-06-18 revisions still advertised),
  bounded by `maxBatchItems` and sharing one deadline. The byte limit is not a limit here:
  a `get_balances` call is under 80 bytes and makes a full round of authenticated requests.
- A bad message is answered and skipped, never fatal.
- The `initialize` instructions tell the model not to present `undocumented`/`no-api`
  figures as authoritative. The confidence level travels with the number into the
  conversation; keep it that way.
- Provider error bodies are stripped from tool results. A tool result is a channel into a
  model's context and an HTTP body is text a remote service chose.

## Dependencies

Exactly one: `gopkg.in/yaml.v3`. JSONPath, the HTTP layer and the MCP server are in-tree on
purpose — a binary that reads billing credentials should have an auditable dependency tree.
CI enforces `go mod tidy -diff` and diffs the linked module list against
`.github/allowed-modules.txt`; adding a module means adding a line there, which is the
review. (It used to print a package count and pass regardless — a gate that only reports is
worse than none, because the green tick claims the property held.) Adding a dependency is a
design decision, not a convenience.

## Testing conventions

**An issue is finished when a test proves it, not when the code works.** The test lands in
the same commit as the change, and it has to fail before the change and pass after — a test
written against already-passing code asserts nothing about the work it was meant to cover,
which is the same objection as the dependency gate that only reported and always passed.
Every issue in the milestone names the test it expects; if implementing it turns out to need
a different one, edit the issue rather than quietly dropping the test.

Two kinds of issue have no behaviour to assert — a documentation refresh and the release tag
itself. Those close with a comment recording what was checked by hand and how, so a missing
test is always a decision someone wrote down, never a silence.

Table-free, behaviour-named tests (`TestSchemaDriftIsAnErrorNotZero`,
`TestSecretStraddlingTheTruncationBoundaryIsRedacted`). Network tests use
`httptest.NewTLSServer` with `fetch.NewWithTransport(ts.Client().Transport, …)`; see the
`harness` helper in `internal/core/check_test.go` and `newSession` in
`internal/mcp/mcp_test.go`. Security-relevant behaviour needs a test that fails loudly if
the protection is removed.

CI matrix is ubuntu/macos/windows, plus a cross-compile job over linux, darwin, windows and
freebsd (amd64/arm64) with `CGO_ENABLED=0` — platform-specific code paths exist in
`internal/secret` (`sh -c` vs `cmd /C`, permission bits) and `internal/core/config.go`
(`%AppData%` vs `XDG_CONFIG_HOME`).
