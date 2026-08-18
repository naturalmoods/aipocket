# AIPocket

**How much prepaid credit is left at each of your LLM API providers — in one place, from one command.**

A single static binary. No runtime, one dependency, and it never stores your API keys.

```
$ aipocket

  PROVIDER      BALANCE  STATE   NOTE
  ──────────  ─────────  ──────  ────────────────────────────────────────────
  DeepSeek    18.42 USD  ok
  Entrim.ai   24.10 USD  ok      inferred field
  Groq        25.00 USD  manual  no balance API; key valid; user-maintained figure
  Neuralwatt   3.75 USD  ok      inferred field
  OpenRouter  37.65 USD  ok      topped up 50.00 / spent 12.35
  ──────────  ─────────  ──────  ────────────────────────────────────────────
  verified    56.07 USD   documented fields only
  inferred    27.85 USD   read from undocumented response shapes
  manual      25.00 USD   your own figures, unverifiable

  outside the verified total: entrim (inferred field), groq (user-maintained), neuralwatt (inferred field)
```

## Why this exists

Most "LLM cost dashboards" track *spend that passes through them*. That is a
different thing from *how much money is left in your account*, and it is the
number you actually want before kicking off a long job.

The awkward truth is that only some providers expose a balance at all. AIPocket's
design commitment is to be honest about which is which.

**There are three totals, not one.** A figure read from a documented field, a
figure inferred from an undocumented response shape, and a figure you typed into
your config are three different kinds of claim. Summing them would give a guess
the same authority as a fact, so `verified` contains only the first kind. Non-USD
balances are reported but never converted — AIPocket has no business guessing an
exchange rate.

The tool also refuses to be confidently wrong. A response that no longer matches
the manifest, a `total - used` that comes out negative, a `NaN` where money
should be: each becomes a visible error, never a green `0.00`.

| confidence | meaning |
|---|---|
| `official` | the provider documents both the endpoint and the field |
| `undocumented` | the endpoint exists, the response shape was inferred — verify it once against the console |
| `no-api` | the provider publishes no balance endpoint; AIPocket checks the key works, the figure is yours to maintain |

## Install

```bash
# Go
go install github.com/naturalmoods/aipocket/cmd/aipocket@latest

# or download a binary for linux / macOS / Windows / FreeBSD
# from the releases page — no runtime required
```

Either way, `aipocket version` reports the release you actually have: a
`go install` build takes it from Go's own build info, so it is never just "dev".
That is the one question worth being able to answer after a security advisory.

## Use

```bash
aipocket                        # all configured providers
aipocket openrouter deepseek    # just these
aipocket --json                 # machine-readable
aipocket providers              # what's known, and where each key is read from
aipocket audit                  # every host that would be contacted, before you run anything
aipocket probe entrim           # look for an undocumented balance endpoint
aipocket probe entrim --dry-run # ...or just list the requests it would make
```

With no configuration at all, AIPocket reads each provider's conventional
environment variable (`OPENROUTER_API_KEY`, `DEEPSEEK_API_KEY`, …). That is
enough for CI and for a first try.

## Configuration

`aipocket config path` prints the location (`~/.config/aipocket/config.yaml` on
Unix, `%AppData%\aipocket\config.yaml` on Windows).

**The config file holds no secrets.** It holds instructions for obtaining them:

```yaml
providers:
  openrouter:
    key: command:op read op://Private/OpenRouter/credential   # 1Password
  deepseek:
    key: command:pass show llm/deepseek                       # pass / gopass
  neuralwatt:
    key: file:~/.secrets/neuralwatt                           # 0600 enforced
  entrim:
    key: env:ENTRIM_API_KEY
    balance_url: https://api.entrim.ai/v1/credits             # see `aipocket probe`
  groq:
    manual: 25.00        # no balance API; reported separately, never in the total
    as_of: 2026-08-01    # optional: when you read it. The row shows the age.
```

Shelling out to whatever secret manager the machine already has is both more
portable and more secure than bundling a keyring library. Concretely: under WSL
there is no D-Bus session and no running keyring daemon, so a cross-platform
keyring dependency simply fails there — while `op`, `pass` and `bw` all work.

## Use from Claude, Cursor, or any MCP client

```bash
claude mcp add aipocket -- aipocket mcp
```

or, in an MCP client config:

```json
{ "mcpServers": { "aipocket": { "command": "aipocket", "args": ["mcp"] } } }
```

Two read-only tools: `get_balances` and `list_providers`. The server tells the
model, in its initialization instructions, not to present an `undocumented` or
`no-api` figure as authoritative — the confidence level travels with the number
all the way into the conversation.

Providers' own error bodies are not forwarded to the model. "Ignore your previous
instructions" is a legal HTTP 402 body, and a tool result is a channel into a
model's context; what travels is AIPocket's own account of the failure, which no
remote service chose the wording of. The table still shows you what the provider
said.

## Security

Short version:

- AIPocket is not a secret store. It resolves credentials at runtime and holds
  them in memory for one run. A literal key in the config file is *refused* when
  the file is loaded, and the refusal does not echo it. `env:` names must be
  upper case for that reason: `gsk_…`, `nvapi_…` and `hf_…` are all valid POSIX
  variable names, so accepting one would print a mis-pasted key back out.
- Every resolved secret is registered with a redactor that runs before any
  message is shortened, so a key echoed in a provider's 401 body cannot survive
  as a truncated prefix in a terminal, a CI log or an assistant transcript.
  There are tests for exactly that, including one for a key straddling the
  truncation boundary.
- Provider manifests are compiled into the binary and are **never** fetched at
  runtime. A manifest says where an API key gets sent; a remotely-updatable
  registry would be a "send your credentials here" primitive.
- https only, redirects are refused, response bodies are capped, every request
  has a deadline.
- `aipocket audit` prints the complete network footprint before you trust it with
  anything — including the requests `aipocket probe` would make and any
  `HTTPS_PROXY` your environment imposes, since a proxy becomes another host that
  receives the credential.
- Anything displayed is stripped of control characters. A provider error body is
  bytes of someone else's choosing on their way to your terminal, and an ANSI
  escape in one can repaint the total printed above it.

The single most valuable thing you can do is not cryptographic: **set a spend
limit at the provider**, and use a read-only or scoped key wherever one is
offered. A balance checker holding a full inference key is the actual exposure.

Full threat model and reporting instructions: [SECURITY.md](SECURITY.md).

## Adding a provider

One YAML file. No Go.

```yaml
id: acme
name: Acme AI
status: official
console: https://acme.ai/billing
docs: https://acme.ai/docs/credits
auth: {type: bearer, env: ACME_API_KEY}
balance:
  url: https://api.acme.ai/v1/credits
  amounts:
    - {path: $.credits_remaining, currency: USD}
```

`aipocket probe <provider>` will search the conventional paths on a provider's own
host and print a manifest block ready to paste. See
[CONTRIBUTING.md](CONTRIBUTING.md).

## Compatibility

From `v1.0.0`, four things are contracts and will not break within the major
version. New fields and new providers may be added; nothing listed here will be
renamed, removed, or change meaning.

- **The config file.** A config that works today keeps working.
- **`--json`.** Field names and their meaning are stable, including the split
  between `total_verified_usd`, `total_inferred_usd` and `total_manual_usd`.
- **The MCP tools.** `get_balances` and `list_providers`, their input schemas,
  and the `confidence` value travelling with every figure.
- **Exit codes.** `0` success, `1` at least one provider errored, `2` usage
  error — scripts depend on these.

Three things are deliberately *not* contracts:

- **The table.** It is for humans and will be reformatted whenever that makes it
  clearer. Parse `--json`.
- **Which providers exist, and what each one can report.** Providers are data. A
  provider that ships a balance API gains one; a provider that withdraws one loses
  it, and its `status` changes accordingly. That is the provider's doing, not an
  API break — and it is the reason `confidence` is in the output rather than in
  the documentation.
- **Anything a provider says.** `provider_message` carries a remote service's own
  words. Its presence and shape are stable; its contents are theirs.

## Dependencies

One: `gopkg.in/yaml.v3`. The JSONPath subset, the HTTP layer and the MCP server
are all in-tree — a binary that reads your billing credentials should have a
dependency tree you can audit in an afternoon.

## Licence

MIT.
