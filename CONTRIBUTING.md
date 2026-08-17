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
  "inferred field" in every output format.
- `no-api` — no balance endpoint. Use a `verify:` endpoint so the key can still
  be checked. Must not define `balance:`. Requires `notes:`.

Marking an inferred reading as `official` is the one change that will get a pull
request rejected outright. It is not cosmetic: `official` is the only status
whose figures enter the **verified total**. Mislabelling one moves a guess into
the number users trust, which is the exact failure this project exists to
prevent.

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
