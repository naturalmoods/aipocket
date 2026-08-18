# Security

## Threat model

AIPocket handles credentials that can spend money. It is not a bank, and the
security effort here is deliberately proportionate: cheap, high-value measures
applied thoroughly, rather than ceremony.

**What AIPocket defends against**

| Risk | Mitigation |
|---|---|
| Secrets at rest in a config file | AIPocket stores none. The config holds a `key:` *instruction* (`env:`, `command:`, `file:`), never a value. The scheme is mandatory and the target must be shaped like one: an `env:` name must be upper case, a `file:` path must be a path. Real key formats — `gsk_…`, `nvapi_…`, `hf_…` — are all valid POSIX variable names, so a pattern that merely asked for "letters, digits and underscores" printed a mis-pasted key straight back out as a "variable name". Every refusal is checked at config-load time and none of them echo the value. |
| Secrets leaking into logs, CI output, bug reports, assistant transcripts | Every resolved secret is registered with a redactor, applied to the **full** text before any shortening. Ordering matters: truncate-then-redact lets a key straddling the cut survive as a prefix, which is a real observed failure and now has a dedicated test. `HTTPError.Error()` omits the response body entirely, so the leak does not depend on every future caller remembering to redact. Provider 401 bodies are untrusted output — one real provider echoes part of the submitted key. |
| A failing credential helper printing the token | A helper's stderr is discarded rather than surfaced. A *failing* helper is where a token turns up — a `set -x` trace, a curl error echoing the request — and no redactor can help, because nothing was resolved and so nothing was registered. The error names the helper and its exit status; run it yourself for the rest. |
| A config file exhausting the machine | `file:` reads at most 64 KiB and only from a regular file (a FIFO would block the run before any timeout applied); a `command:` helper is bounded by output size, a 90-second deadline, and a `WaitDelay` for the case where the helper exits but a grandchild still holds the pipe. |
| A provider error body driving the terminal | Control characters, ANSI/OSC sequences and bidi overrides are stripped from anything displayed. An escape can repaint the screen, so a 402 body could overwrite the verified total printed above it; `U+202E` can display a reversed number. |
| A provider error body reaching a model as instructions | The provider's words live in their own field and the MCP server does not forward it. "Ignore your previous instructions" is a legal HTTP 402 body, and a tool result is a channel into a model's context. The tool's own account of the failure — status code, schema mismatch — is what travels, and it is not attacker-controlled. |
| A typo silently disabling nothing | Every provider id in the config is checked against the compiled-in registry. `openruter: {disabled: true}` used to be accepted while openrouter kept being contacted with the key the user believed they had withdrawn. |
| A `NaN`, `Inf` or hex-float amount poisoning the report | Rejected in `internal/jpath`. `strconv.ParseFloat` accepts all three; a `NaN` total makes the whole JSON document unencodable, so one broken provider would erase every other provider's balance. |
| A schema change reported as a confident number | A response that matches no amount, and a `total - used` that comes out negative, both become visible errors rather than a green `0.00` or a negative balance that quietly reduces the total. |
| A manifest redirecting a key to an attacker | Manifests are compiled into the binary and never fetched at runtime. `https` and "no userinfo in URL" are enforced at load time, so a bad manifest fails the test suite, not a user. |
| A provider redirecting a key mid-request | Redirects are never followed. A 3xx is an error. |
| Hostile or broken endpoint exhausting the host | 1 MiB response cap, per-request deadline. |
| Supply-chain compromise via dependencies | One dependency (`gopkg.in/yaml.v3`). JSONPath, HTTP and MCP are in-tree. |
| A world-readable credential file | `file:` sources refuse a mode with any group/other bits (Unix). |
| Not knowing what the tool talks to | `aipocket audit` prints every request and host before you run anything — including the `aipocket probe` requests, which only run when asked but do send the key, and including an `HTTPS_PROXY` from your environment, which becomes a host that receives every credential. `aipocket probe --dry-run` lists a probe's requests without sending them. A disabled provider is contacted by nothing, not even an explicit `aipocket probe`. |
| An MCP client spending unbounded work | A batch is capped at 32 operations and shares one deadline. The 4 MiB line limit is not a limit here: a `get_balances` call is under 80 bytes and makes a full round of authenticated requests to every provider. |
| A release built by an unreviewed workflow run | The test suite runs in a separate read-only job; only the release job holds `contents: write` and an OIDC identity, and it runs behind a protected environment. Actions are pinned to commit SHAs, not tags. Archives are uploaded as a draft and published only after the provenance attestation exists — published first, every download in that window fails verification exactly as a tampered file would. |

**What AIPocket does not defend against, by design**

- **A hostile config file.** `key: command:...` runs a shell command. Anyone who
  can write your config can already run code as you; treating it as a sandbox
  boundary would be theatre. Keep the config file as trusted as the binary.
- **A compromised secret manager.** AIPocket delegates; it does not verify.
- **Memory-resident secrets.** Keys live in process memory for the run's
  duration. Go gives no reliable way to scrub them, and pretending otherwise
  would be worse than saying so.
- **Partial or re-encoded echoes of a key.** The redactor matches whole values.
  A provider that returns your key split across fields, base64-encoded, or with
  characters escaped would defeat it. Full-value echo — the case seen in
  practice — is covered; this residual gap is real and stated rather than
  papered over.
- **Provider-side compromise.** Out of scope.

## The measure that matters most

It is not cryptographic. **Set a spend limit at the provider**, and use a
read-only or billing-scoped key wherever one is offered. A balance checker
holding a full inference key is the real exposure — not where that key is
stored.

If a balance endpoint returns 401/403 while inference works, that is usually
scope, not a wrong key. AIPocket says so in the error.

## Release protections

Two of the protections above cannot be expressed in the workflow file, so they
belong to the release procedure and are recorded here:

- **The `v*` tags are protected by a repository ruleset.** Pushing such a tag
  starts a job that holds `contents: write` and an OIDC identity capable of
  attesting that its output is an official build. Anyone who can push a tag can
  therefore publish a release in this repository's name.
- **The `release` environment requires a reviewer.** This gates the job itself
  rather than the tag, so a tag pushed by mistake does not publish anything.

If you are forking or taking over this repository, set both up before the first
release. `.github/workflows/release.yml` assumes they exist — and GitHub creates a
missing environment silently, with no protection rules, the first time a job
references it, so their absence looks exactly like their presence.

`.github/setup-release-protections.sh` applies both and then reads the result
back. It is idempotent, and `--dry-run` prints every request without sending it:

```bash
.github/setup-release-protections.sh --dry-run     # see what it would do
.github/setup-release-protections.sh               # apply
.github/setup-release-protections.sh --restrict-creation   # also gate tag creation
```

Two details it encodes rather than leaves to memory:

- **The tag rules are `update` and `deletion`, not `creation`, by default.** Those
  two are what make a published tag immutable, which is what the provenance
  attestation depends on: if `v1.2.3` can be moved, verifying a download against
  it proves nothing. Restricting *creation* only bites once someone other than the
  owner has write access, and a wrong bypass actor locks the owner out of tagging
  — discovered at release time. Hence the flag.
- **`prevent_self_review` is off with a single reviewer.** When the only reviewer
  is also the person who pushes tags, enabling it makes the release approvable by
  nobody. What the gate still buys is real: publishing requires a human to click
  approve, which a leaked token pushing a tag cannot do.

Note that a repository admin can always edit or delete a ruleset. This is a
speed bump against mistakes and against a token that can push, not a control that
survives a compromised admin account — no repository setting is.

## Reporting a vulnerability

**Open a private security advisory on GitHub**
(*Security → Advisories → Report a vulnerability*). That is the only channel:
this file used to offer "or email the maintainer" without giving an address,
which is not a second option but a dead end that could send someone to a public
issue instead.

Please do not open a public issue for anything involving credential exposure.
