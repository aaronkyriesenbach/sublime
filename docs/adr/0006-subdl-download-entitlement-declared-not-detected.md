# SubDL download entitlement is declared in config, not detected at runtime

SubDL has two download paths: an anonymous one (no `api_key` on the request,
300 downloads/day per IP, available to any account) and an authenticated one
(`api_key` attached, draws from the account's paid download quota) — but
authenticating a request from a free-tier account gets rejected with `402
paid_api_required`. Every SubDL account, free or paid, already has an
`api_key` (search requires it unconditionally), so the key's mere presence
says nothing about which download path it's entitled to use.

Sublime does not infer this at runtime. `providers.subdl.paid` is a plain,
user-declared config value: attach the key on downloads when true, omit it
when false. A `402` despite `paid: true` surfaces as an ordinary `Download`
error — a misconfiguration to fix, not a signal to silently retry anonymously.
This is also why `provider_chain: [{name, worker_count}]` was widened into a
`providers: {chain: [...], <name>: {...}}` block: non-secret, Provider-
specific settings like `paid` needed a home that wasn't the env-var-only
`ProviderSecrets` (meant strictly for credentials) or the ordering-only
`chain` list.

**Considered options:**

- Learn it reactively: attempt an authenticated download first, and on the
  first `402`, remember "this key isn't entitled" in memory and fall back to
  anonymous for the rest of the process. Rejected: adds a mutex-guarded
  tri-state flag and 402-specific retry branching to what would otherwise be
  a stateless request/response Provider, to avoid declaring one bool.
- Check proactively via SubDL's `GET /me` account-status endpoint once at
  Provider construction. Rejected: adds a second endpoint dependency and its
  own failure handling (what happens if `/me` itself fails?) for the same
  fact a user already knows and can just state.
