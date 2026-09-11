# Fixture provenance

Captured 2026-09-11 while implementing the 0.3 compat client (M4).

## Live captures (verbatim)

| File | Source | How |
|---|---|---|
| `thehiveryiq-card.json` | `GET https://thehiveryiq.com/.well-known/agent.json` | curl, saved verbatim |
| `rosentic-card.json` | `GET https://api.rosentic.com/.well-known/agent.json` | curl, saved verbatim |
| `humanbrowser-card.json` | `GET https://agent.humanbrowser.cloud/.well-known/agent-card.json` | curl, saved verbatim |
| `humanbrowser-unauthorized-error.json` | `POST https://agent.humanbrowser.cloud/a2a` (`message/stream`, no bearer token) | HTTP 401 response body, saved verbatim |

Live-messaging status on capture day (all three agents' message endpoints
were exercised once each with a benign "Reply with the single word:
pong." request, so no stream fixture could be captured live):

- `thehiveryiq.com`: card `url` is the bare origin; POSTing JSON-RPC
  there returns HTTP 200 with an empty body (static host; `rndr-id`
  header) — the A2A backend is dead behind a live card.
- `api.rosentic.com`: card `url` `/a2a` is a working JSON-RPC endpoint,
  but `message/send`, `message/stream`, and even `SendMessage` all
  answer `-32601 Method not found`.
- `agent.humanbrowser.cloud`: streams require a bearer token; the 401
  body is the captured JSON-RPC error (note: the server reuses code
  `-32001` — the 0.3 TaskNotFound code — for "Unauthorized", and adds a
  non-standard `data.hint` member).

## Spec-derived (synthesized)

| File | Basis |
|---|---|
| `send-task-result.json` | A2A v0.3.0 spec §7.1/§6.1 (message/send result = bare Task, `kind:"task"`), IDs from the §9.3 example |
| `send-message-result.json` | A2A v0.3.0 spec §7.1 (result = bare Message, `kind:"message"`) |
| `stream.sse` | A2A v0.3.0 spec §9.3 streaming example (task snapshot → status-update → artifact-updates with append/lastChunk → final status-update), compacted to one `data:` line per frame plus tolerated `event:`/`id:`/comment lines |
| `input-required.sse` | A2A v0.3.0 spec §9.4 multi-turn example (final status-update in `input-required`) |

Spec source: `https://raw.githubusercontent.com/a2aproject/A2A/v0.3.0/docs/specification.md`
and `.../v0.3.0/types/src/types.ts` (authoritative field names, e.g.
`FileWithBytes.bytes` vs the example's `file.data`, and
`TaskPushNotificationConfig {taskId, pushNotificationConfig}`).

## Wire facts pinned by these fixtures

- `message/send` JSON-RPC result is the **bare** Task or Message object
  (no `{task:…}`/`{message:…}` wrapper), discriminated by `kind`.
- SSE frames are bare `data:` lines, each a complete JSON-RPC response;
  the stream terminates on the `final:true` status-update.
- File parts: schema `{"kind":"file","file":{"bytes"| "uri","mimeType","name"}}`;
  the spec's own §9.3 example instead spells the base64 field `data`, so
  the client accepts `bytes`, `data`, and `uri` defensively.
- 0.3 error codes observed/specced: -32001..-32005 plus stock
  -32700..-32603; live servers may reuse spec codes with unrelated
  messages (humanbrowser's 401).
