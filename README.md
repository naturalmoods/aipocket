# AIPocket

**How much prepaid credit is left at each of your LLM API providers — in one place, from one command.**

A single static binary. No runtime, one dependency, and it never stores your API keys.

```
$ aipocket

  PROVIDER               BALANCE  STATE   NOTE
  ──────────────────  ──────────  ──────  ────────────────────────────────────────────
  Anthropic                    —  manual  no balance API; key valid
  DeepSeek             18.42 USD  ok
  fal.ai               24.50 USD  ok
  Groq                 25.00 USD  manual  no balance API; key valid; user-maintained figure (2026-08-01, 17 days ago)
  Moonshot AI (Kimi)   49.59 USD  ok
  OpenAI                       —  manual  no balance API; key valid
  OpenRouter           37.65 USD  ok      topped up 50.00 / spent 12.35
  SiliconFlow          12.80 USD  ok      inferred field
  ──────────────────  ──────────  ──────  ────────────────────────────────────────────
  verified            130.16 USD   documented fields only
  inferred             12.80 USD   read from undocumented response shapes
  manual               25.00 USD   your own figures, unverifiable

  outside the verified total: siliconflow (inferred field), groq
    (user-maintained)

  11 providers have no credential configured: cerebras, deepinfra, entrim,
    gemini, mistral, nebius, neuralwatt, novita, replicate, together, xai
    (--all)
```

The figures are illustrative; the layout is not — it is printed by the same
renderer the binary uses. Providers with no credential configured collapse into
that last line rather than filling the table with rows saying a variable is not
set; `--all` brings them back.

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

## Providers

Twenty, and the honest summary of the state of LLM billing APIs is the
`confidence` column: **five** providers publish a balance endpoint you can trust,
two answer one whose response shape had to be inferred, and thirteen publish
nothing at all — for those AIPocket checks the key and reports whatever figure you
choose to keep.

| provider | id | confidence | reports |
|---|---|---|---|
| Anthropic | `anthropic` | no-api | key check only |
| Cerebras | `cerebras` | no-api | key check only |
| DeepInfra | `deepinfra` | no-api | key check only |
| DeepSeek | `deepseek` | official | balance |
| Entrim.ai | `entrim` | no-api | key check only |
| fal.ai | `fal` | official | balance |
| Google Gemini | `gemini` | no-api | key check only |
| Groq | `groq` | no-api | key check only |
| Mistral AI | `mistral` | no-api | key check only |
| Moonshot AI (Kimi) | `moonshot` | official | balance |
| Nebius Token Factory | `nebius` | no-api | key check only |
| Neuralwatt | `neuralwatt` | undocumented | balance (inferred) |
| Novita AI | `novita` | official | balance |
| OpenAI | `openai` | no-api | key check only |
| OpenRouter | `openrouter` | official | balance |
| Replicate | `replicate` | no-api | key check only |
| SiliconFlow | `siliconflow` | undocumented | balance (inferred) |
| Straitly | `straitly` | no-api | key check only |
| Together AI | `together` | no-api | key check only |
| xAI | `xai` | no-api | key check only |

A provider that ships a balance API gains one here; that is data, not a release.
Adding one is a single YAML file — see [Adding a provider](#adding-a-provider).

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
aipocket --all                  # include the ones with no credential configured
aipocket --json                 # machine-readable
aipocket providers              # what's known, and where each key is read from
aipocket audit                  # every host that would be contacted, before you run anything
aipocket probe entrim           # look for an undocumented balance endpoint
aipocket probe entrim --dry-run # ...or just list the requests it would make
aipocket config add             # pick a provider, say where its key comes from
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

`aipocket config add` writes that block for you to paste: it lists the providers
this build knows, asks which of the three forms the key comes from, and checks the
answer with the same rule the config loader applies. It prints — it does not edit
the file, and it never asks for the key. Re-marshalling the YAML would drop the
comments that explain each entry, and a subcommand that took a key would put it in
the shell history on its way into the one file that promises to hold none.

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
