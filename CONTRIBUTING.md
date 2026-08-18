# Contributing

## Adding a provider

Providers are data. Adding one is a single YAML file in `providers/` — no Go
changes, no new code paths to review.

1. Write `providers/acme.yaml`. If the provider documents a balance endpoint, use
   it. If it does not, start with `status: no-api` and just a `verify:` endpoint —
   that is enough to get to step 2.

   ```yaml
   id: acme                       # lowercase, matches the filename
   name: Acme AI
   status: official               # official | undocumented | no-api
   console: https://acme.ai/billing
   docs: https://acme.ai/docs/credits    # required when status is official
   auth:
     type: bearer                 # bearer | header
     env: ACME_API_KEY           # UPPER_CASE; it is printed by `aipocket audit`
   balance:
     url: https://api.acme.ai/v1/credits
     amounts:
       - {path: $.credits_remaining, currency: USD}
   ```

2. If you had to guess the endpoint, let `probe` look for it:

   ```bash
   export ACME_API_KEY=...
   go run ./cmd/aipocket probe acme --dry-run   # what it would send
   go run ./cmd/aipocket probe acme             # send it
   ```

   The manifest has to exist first, and that ordering is the design rather than an
   inconvenience: `probe` only ever contacts a host already named in that
   provider's own manifest, so no command-line argument can aim your key at an
   arbitrary server. It prints a manifest block to paste back into step 1.

3. `go test ./...`

### Picking a status honestly

This is the part that matters most, and the tests enforce it.

- `official` — the provider documents the endpoint **and** the response field.
  Requires a `docs:` link.
- `undocumented` — the endpoint exists but the response shape is inferred.
  Requires `notes:` saying what was guessed. The reading is labelled
  "inferred field" in every output format. Compare it once against the provider's
  own console and record the date in `notes:` — and expect the console to round.
  Neuralwatt's portal shows two decimals where its API returns three, so a
  difference in the last decimal is display rounding and not a different field. A
  mismatch in the *first* decimal is the signal that the path names something else.
  Record that the comparison happened, never the figure: a balance in a manifest is
  someone's money in a public git history.
- `no-api` — no balance endpoint. Use a `verify:` endpoint so the key can still
  be checked. **Check that the endpoint actually requires the credential** — see
  below; two candidates have failed this. Must not define `balance:`. Requires
  `notes:`, and the note has to record when you looked: `checked 2026-08-17: the billing FAQ documents no
  balance endpoint and directs users to the console`. A test enforces the date.
  This is the one claim in the registry that no test can verify — it is about a
  provider's documentation, not about this code — and the only one that rots on
  its own, so the date is what lets a reader judge whether it is still true.

Marking an inferred reading as `official` is the one change that will get a pull
request rejected outright. It is not cosmetic: `official` is the only status
whose figures enter the **verified total**. Mislabelling one moves a guess into
the number users trust, which is the exact failure this project exists to
prevent.

### A verify endpoint has to require the credential

Test it before you commit it, with no key at all:

```bash
curl -s -o /dev/null -w '%{http_code}\n' https://api.acme.test/v1/models   # want 401
```

A models list is the obvious choice for a key check and it is sometimes the wrong
one. DeepInfra documents theirs as needing no authentication; Straitly's answers
`200` to a request with no credential and to one with a garbage key. Verifying
against either would print **"key valid" for a key that had been revoked** — the
tool being confidently wrong about the one thing it can tell you for a provider
with no balance API, which is worse than saying nothing.

If the only authenticated GET is an odd one, use it and say why in `notes:`.
`straitly.yaml` reads an activity export with its `since` parameter set far in the
future, so the request returns no rows: the only thing wanted from it is the
authentication result.

### The Authorization scheme

Most providers want `Authorization: Bearer <key>`, which is the default. fal wants
`Authorization: Key <key>`:

```yaml
auth:
  type: bearer
  scheme: Key        # optional; Bearer when omitted
  env: FAL_KEY
```

Checked at load time: one token of letters, digits and hyphens, so a data file
cannot end the header line and start another. `scheme` with `type: header` is a
load error rather than a silent no-op — there the credential is written raw into
the header you name, and a scheme would mean nothing.

### Static headers

Some providers require a header on every request that has nothing to do with the
credential — Anthropic's `/v1/models` answers `400` without `anthropic-version`,
so without it the row would report "key check failed" for a perfectly good key:

```yaml
auth:
  type: header
  header: x-api-key
  headers: {anthropic-version: "2023-06-01"}
  env: ANTHROPIC_API_KEY
```

Literal values only, and **never a credential**. A header whose value is a secret
is a second credential, and the redactor, `aipocket audit` and `aipocket
providers` would all have to learn about it; open an issue instead of stretching
this field.

Checked at load time: names match `[A-Za-z0-9-]+`, values are non-empty printable
ASCII, and you cannot set `Authorization`, `Accept`, `User-Agent` or whatever
`auth.header` names — the first would displace the key, the rest are aipocket's
own and would be silently ignored. `aipocket audit` prints these headers with the
request they belong to, because they are part of what gets sent.

### The amounts list

Candidates, tried in order; the first that resolves is reported.

```yaml
amounts:
  # remaining balance directly
  - {path: $.credits_remaining, currency: USD}
  # or lifetime figures, subtracted for you
  - {total: $.data.total_credits, used: $.data.total_usage, currency: USD}
  # or per-currency wallets
  - {path: "$.balance_infos[?currency=USD].total_balance", currency: USD}
  - {path: "$.balance_infos[?currency=CNY].total_balance", currency: CNY}
```

Non-USD balances are reported but excluded from the verified total. AIPocket does
not convert currencies; it has no business guessing a rate.

A provider that reports fixed-point units needs `scale:` — Novita's figures are
integers in 1/10000 USD:

```yaml
amounts:
  - {path: $.availableBalance, scale: 0.0001, currency: USD}
```

It must be finite and greater than zero, and it is checked at load time. Zero is
the one that matters: it would report every account as empty, which is the most
dangerous wrong answer this tool can give. Note that `money.Plausible` is no
substitute for the scale — a million dollars is a perfectly plausible amount of
money, so an unconverted figure sails straight through it and into the total.

### JSONPath subset

`$.a.b`, `$.a[0]`, `$.a[?field=value]`, and combinations. Anything else fails at
load time. If you need more, open an issue before extending the evaluator — the
small surface is deliberate.

## Code

- `go test ./...` and `go vet ./...` must pass; CI also runs `-race`.
- New behaviour needs a test. Security-relevant behaviour needs a test that
  fails loudly if the protection is removed.
- Dependencies: the bar is very high. The current count is one. CI diffs the
  linked module list against `.github/allowed-modules.txt`, so adding one means
  adding a line there in the same pull request — that line is the review.
