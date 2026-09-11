# Live agents for manual testing

Public A2A agents are useful for exercising a2a-tui against real servers
(and real-world protocol drift). The table below lists agents verified
during development, what dialect they speak, and their quirks.

**These are third-party services. They change, move behind auth, start
charging, or disappear without notice.** Re-verify any of them at any time
with the smoke test:

```sh
make smoke                    # card fetch + one benign "pong" message per agent
make smoke ARGS=--strict      # also fail on any non-skipped agent failure
scripts/smoke.sh --strict     # equivalent, direct invocation
```

The smoke test exits 0 when at least one agent passes both phases (card +
one message); `--strict` additionally fails on any non-skipped agent
failure. Auth-protected agents are skipped unless a per-agent token is set
(`SMOKE_TOKEN_<NAME>`, e.g. `SMOKE_TOKEN_HUMANBROWSER=... make smoke`),
attached as `Authorization: Bearer <token>`.

## Verified agents

Card path is relative to the agent origin. "Send" is the smoke test's
single benign message, in the dialect the agent's card declares.

| Agent | URL | Card path | Protocol | Streaming | Auth | What it does | Last verified | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Money You're Owed | `https://agent.moneyyoureowed.com` | `/.well-known/agent-card.json` | v1.0 | no | none | Q&A about unclaimed money, deposits, refunds | 2026-09-11 (working) | The most reliable open v1.0 agent; replies arrive as data parts. Card has no top-level `url` — the POST target comes from `supportedInterfaces[0].url`. |
| IdeaTrace | `https://ideatrace-a2a-server.vercel.app` | `/.well-known/agent-card.json` | v1.0 | no | none (card) | Idea tracing, script writing, monetization research | 2026-09-11 (card only) | Sends now return HTTP 402 with x402/Alipay payment options — the card and card fetch still work, the wire is paywalled. |
| Agent Ready | `https://agent-ready.dev` | `/.well-known/agent-card.json` | v1.0 (card) | no | bearer | Website security scans | 2026-09-11 (drifted) | **Drift example:** the card declares `protocolVersion 1.0`, but the endpoint (`/api/v1/a2a`) rejects `SendMessage` with `-32601` and happily answers `message/send` (0.3 dialect). Connect with `--protocol 0.3`. |
| Priorflow / agentopt | `https://agentopt.app` | `/.well-known/agent-card.json` | v1.0 | no | apiKey | Agent/service catalog selection | 2026-09-11 (card only) | The A2A runtime is currently disabled on the deployment: POST to the card URL returns 405, `/a2a` answers "a2a_runtime_disabled". Card fetch only. |
| Hive Civilization | `https://thehiveryiq.com` | `/.well-known/agent.json` (also served at the v1.0 path) | v0.3.0 | yes | none | Agent passports, cargo classification, on-chain transfers (game-like) | 2026-09-11 (flaky) | The card's `url` points at the bare origin, and POSTs there return **empty HTTP 200s** — the wire is currently broken even though the card fetches fine. Historically streamed. |
| Rosentic | `https://api.rosentic.com` | `/.well-known/agent.json` (also served at the v1.0 path) | v0.3.0 | yes | none | Git branch/conflict analysis and merge assistance | 2026-09-11 (flaky) | Card lists `/a2a` as the POST target, but `message/send` there answers `-32601 Method not found`. Card fetch only for now. |
| humanbrowser | `https://agent.humanbrowser.cloud` | `/.well-known/agent-card.json` (the 0.3 path 404s) | v0.3.0 | yes | bearer | Browser automation: navigate, scrape, fill forms, logins | 2026-09-11 (auth-gated) | Card is large (~57 KB) and served at the v1.0 path despite being a 0.3 card; push notifications supported. `message/send` without a token returns a proper JSON-RPC `-32001 Unauthorized`. Set `SMOKE_TOKEN_HUMANBROWSER` to test the wire. |

## Pointing a2a-tui at them

```sh
a2a-tui --agent https://agent.moneyyoureowed.com            # v1.0, auto-detected
a2a-tui --agent https://thehiveryiq.com                     # v0.3, auto-detected
a2a-tui --agent https://agent-ready.dev --protocol 0.3      # force a dialect
a2a-tui --agent ready                                      # saved agent name
```

Inside the app: `/connect <url-or-name>` (version auto-detection happens
per card), `/card` for the card viewer, `/stream on` for SSE sends against
streaming agents, `/agent save <name>` to keep one around.

Tokens for authenticated agents belong in
`~/.config/a2a-tui/credentials.json` (see the README's configuration
section) — never on the command line.

## Drift is the point

Every quirk in the table above — a v1.0 card on a 0.3 wire, payment
walls, empty 200s, runtime-disabled deployments — was found by this
project's tooling. If an agent here stops behaving, that is expected:
update the table, note the date, and rely on the fixture agent
(`cmd/a2a-fixture-agent`) for deterministic testing.
