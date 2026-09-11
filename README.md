# a2a-tui

A terminal client for agents that speak the
[A2A protocol](https://a2a-protocol.org) (v1.0 and v0.3) and render
[A2UI](https://a2ui.org) surfaces — built in Go with
[Bubble Tea](https://github.com/charmbracelet/bubbletea).

a2a-tui is both a **user client** (chat with any A2A agent, watch tasks
stream live, fill in agent-hosted forms) and a **testing tool** for the
protocol itself (agent-card inspection, wire capture, protocol-version
visibility, a scripted fixture agent, and a live-agent smoke test).

<!-- Screenshots: add real captures at v0.1.0 (streaming chat, card pane,
     interactive A2UI form). No fabricated images before then. -->

## Features

- **A2A v1.0** via the official [`a2a-go`](https://github.com/a2aproject/a2a-go)
  SDK: agent-card discovery (`/.well-known/agent-card.json`), blocking and
  SSE streaming sends (`SendMessage` / `SendStreamingMessage`), task state,
  artifacts, history, cancel.
- **A2A v0.3 compat** via a hand-rolled client (`internal/compat03`): the
  public ecosystem still has many 0.3 agents — including most of the
  streaming ones. The protocol version is auto-detected from the agent
  card; `--protocol` forces a dialect.
- **Async task lifecycle**: streaming with automatic reconnect
  (`SubscribeToTask` backoff, visible status lines), a tasks dashboard over
  an event-fed registry (detail / cancel / subscribe / refresh), and
  **push notifications** — a built-in loopback webhook listener with
  per-task registration (`/push on`, tunnel support via
  `--push-public-url`).
- **Version-agnostic app**: both clients translate into the same types, so
  chat, tasks, streaming and A2UI work the same against either dialect.
- **A2UI in the terminal** — the first terminal renderer for A2UI: all 18
  canonical components, rendered inline in the transcript and, on
  `ctrl+f`, as a focused interactive pane (text fields, checkboxes,
  pickers, sliders, date-time inputs, buttons). Inputs bind locally to the
  surface data model; a Button press sends the action envelope back to the
  agent, whose `updateComponents` / `updateDataModel` envelopes mutate the
  live surface. Unknown components degrade gracefully.
- **Testing tooling**: agent-card viewer (skills, capabilities, security
  schemes), raw wire capture with a wire pane and a raw-frame peek on
  every error, a raw JSON-RPC console for arbitrary methods (dialect-aware
  presets, `A2A-Version` toggle, history), A2A error codes surfaced by
  name, protocol version always visible in the header, a deterministic
  fixture agent, and `make smoke` against live public agents.

Untrusted-content hardening throughout: agent text is ANSI-stripped and
size-capped, media URLs are never fetched, and `openUrl` actions are gated
behind a y/n prompt (which never executes anything — it only echoes the
URL).

## Status

v0.1.0 — all nine milestones landed (history in the
[issue tracker](https://github.com/HenryNebula/a2a-tui/issues)):

| Milestone | Scope | Status |
| --- | --- | --- |
| M1 | scaffold, repo, CI | done |
| M2 | config, card resolution, card viewer | done |
| M3 | chat core on v1.0 | done |
| M4 | 0.3 compat, tasks dashboard, wire log | done |
| M5 | fixture agent + e2e harness | done |
| M6 | push notifications | done |
| M7 | A2UI engine + static rendering | done |
| M8 | A2UI interactive | done |
| M9 | raw console, polish, v0.1.0 | done |

## Install / run

Requires Go 1.26+ (see `go.mod`).

```sh
go install github.com/HenryNebula/a2a-tui/cmd/a2a-tui@latest
a2a-tui --agent https://agent.moneyyoureowed.com
```

Or from a checkout:

```sh
make run                                  # go run ./cmd/a2a-tui
go run ./cmd/a2a-tui --agent https://thehiveryiq.com     # a 0.3 agent
go run ./cmd/a2a-tui --agent http://127.0.0.1:8877       # the fixture agent
```

| Flag | Values | Meaning |
| --- | --- | --- |
| `--agent` | URL or saved agent name | connect at startup |
| `--protocol` | `1.0`, `0.3`, `auto` (default) | force a wire dialect or detect it from the agent card |
| `--push-public-url` | URL | public URL for the push-notification webhook listener (for tunnels; also `A2A_TUI_PUSH_URL`) |

Without `--agent` the app starts disconnected; use `/connect` inside.

## Keys

Global:

| Key | Action |
| --- | --- |
| `enter` | send message / run slash command |
| `alt+enter`, `ctrl+j` | newline in the input box |
| `esc` | cancel the active send/stream; leave the surface/console pane |
| `ctrl+t` | transcript pane |
| `ctrl+g` | agent-card pane (scroll: `up`/`down`, `pgup`/`pgdown`, `home`/`end`) |
| `ctrl+k` | tasks dashboard (cursor: `up`/`down`; `enter` detail, `c` cancel, `s` subscribe, `r` refresh) |
| `ctrl+w` | wire pane: raw framed request/response traffic (`c` clears) |
| `ctrl+e` | raw JSON-RPC console |
| `ctrl+f` | focus the most recent A2UI surface |
| `?` | help overlay (full key + command reference) |
| `ctrl+c`, `ctrl+d` | quit |

Raw JSON-RPC console pane:

| Key | Action |
| --- | --- |
| `tab`, `shift+tab` | cycle method presets for the connected dialect |
| `up` / `down` (method field) | cycle the last 50 exchanges |
| `ctrl+enter` (`alt+enter`, `ctrl+s`) | send the request |
| `ctrl+v` | toggle the `A2A-Version` header (auto → on → off) |
| `pgup`/`pgdn` | scroll the response |

A2UI surface pane:

| Key | Action |
| --- | --- |
| `tab`, `shift+tab` | cycle focus between inputs/buttons |
| type | edit the focused text/date-time field; filter a filterable picker |
| `space` / `enter` | press button, toggle checkbox, select picker option |
| `up` / `down` | move picker cursor; adjust slider by 10%; scroll when not on an input |
| `left` / `right` | adjust slider by one step |
| `[` / `]` | switch between live surfaces |
| `y` / `n` | answer the gated `openUrl` prompt (opening is disabled; the URL is echoed for copying) |

## Commands

| Command | Arguments | Description |
| --- | --- | --- |
| `/help` | | command reference |
| `/connect` | `<url-or-name>` | resolve a card and connect (auto-detects the protocol) |
| `/disconnect` | | tear down the session |
| `/agents` | | list saved agents |
| `/agent save` | `<name>` | save the current connection under a name |
| `/agent remove` | `<name>` | remove a saved agent |
| `/agent default` | `<name>` | mark the default saved agent |
| `/card` | | open the agent-card pane |
| `/surface` | `[id]` | focus an A2UI surface (default: most recent) |
| `/chat` | | back to the transcript pane |
| `/tasks` | | open the tasks dashboard |
| `/console` | | open the raw JSON-RPC console |
| `/push` | `[on\|off]` | start/stop the push-notification webhook listener + per-task registration |
| `/stream` | `[on\|off]` | toggle streaming (`SendStreamingMessage` SSE) vs blocking sends |
| `/wire` | `[on\|off]` | toggle raw wire capture (frames buffered for the wire pane) |
| `/task` | `[id]` | fetch a task snapshot into the transcript (default: last task) |
| `/cancel` | `[id]` | cancel a task (default: last task) |
| `/history` | `<id> [n]` | fetch a task with `historyLength` |
| `/clear` | | clear the transcript |
| `/quit` (`/exit`) | | quit |

When a task parks in `input-required`, the next plain message is attached
to that task (taskId + contextId) automatically.

## Fixture agent quickstart

`cmd/a2a-fixture-agent` is a deterministic local A2A 1.0 agent for
exercising the client without network or third-party drift:

```sh
go run ./cmd/a2a-fixture-agent            # listens on 127.0.0.1:8877
go run ./cmd/a2a-fixture-agent --addr 127.0.0.1:9901
a2a-tui --agent http://127.0.0.1:8877
```

Behavior is scripted by the first keyword of each message
(case-insensitive):

| Keyword | Behavior |
| --- | --- |
| `stream <n>` | working status, then n streamed chunks, then completed |
| `artifact` | completed with a markdown text artifact |
| `artifact append` | artifact grown through append (`lastChunk`) events |
| `inputreq` | input-required question; answering on the same task completes it |
| `push` | slow status updates; register a push webhook to receive them |
| `slow <seconds>` | working, a cancelable sleep, completed |
| `fail` | failed task |
| `cancelme <seconds>` | long working phase; CancelTask moves it to canceled |
| `a2ui form` | contact-form surface exercising every input component |
| `a2ui dynamic` | formatString-bound text plus a live-update button |
| `a2ui list` | list template over a five-element data array |
| `skills` | this keyword list |
| anything else | `echo: <text>` |

## Live agents

A curated table of public agents (v1.0 and 0.3, open and auth-gated), what
each does, and their current quirks lives in
[docs/live-agents.md](docs/live-agents.md). They drift without notice;
re-verify with:

```sh
make smoke                 # card fetch + one benign message per agent
make smoke ARGS=--strict   # fail on any non-skipped agent failure
```

## Configuration

User config lives under `~/.config/a2a-tui/` (per
[`os.UserConfigDir`](https://pkg.go.dev/os#UserConfigDir) on your platform)
and is never read from or written to the repository. Writes are atomic
(temp file + rename).

`config.json` — saved agents (written by `/agent save`, `/agents`, …):

```json
{
  "version": 1,
  "default_agent": "ready",
  "agents": [
    { "name": "ready", "url": "https://agent-ready.dev", "protocol": "0.3" }
  ]
}
```

`credentials.json` — per-agent credentials, mode 0600. A value may be a
literal or an `env:VARNAME` reference (the secret stays out of the file;
references resolve from the environment at connect time):

```json
{
  "tokens": {
    "ready": { "scheme": "bearer", "value": "env:A2A_READY_TOKEN" },
    "humanbrowser": { "scheme": "bearer", "value": "env:HUMANBROWSER_TOKEN" }
  }
}
```

`scheme` is `bearer` (`Authorization: Bearer …`) or `apikey` (custom
`header` optional). Credentials are keyed by agent name or URL.

## Development

```sh
make build    # go build -o bin/a2a-tui ./cmd/a2a-tui
make run      # go run ./cmd/a2a-tui
make test     # unit tests
make race     # go test -race ./...
make lint     # golangci-lint run
make fmt      # gofmt -l -w .
make ci       # gofmt check + go vet + test -race (what CI runs)
make smoke    # live-agent smoke test (network; see docs/live-agents.md)
make clean
```

CI (GitHub Actions) runs the gofmt check, `go vet`, `go test -race ./...`
and golangci-lint on every push/PR.

Tests live in-package next to the code. Notable layouts:

- Golden fixtures: `internal/a2ui/testdata/` (A2UI spec examples) and
  `internal/compat03/testdata/` (captured live 0.3 wire frames).
- Fuzzing: `internal/compat03/sse_fuzz_test.go` (hostile SSE input).
- Hostile-input tests for the A2UI engine (`internal/a2ui/hostile_test.go`).
- Live tests, never run by CI — need the `live` build tag *and* an env
  opt-in, e.g. `A2A_TUI_LIVE=1 go test -tags live ./...`.

## Project layout

```
cmd/a2a-tui/            terminal client (flags: --agent, --protocol)
cmd/a2a-fixture-agent/  deterministic local A2A 1.0 test agent
internal/
  a2ui/                 A2UI engine: envelopes, components, dynamic values,
                        surfaces, function catalog (pure, stdlib-only)
    jsonptr/            RFC 6901 JSON Pointer (data-model updates)
    render/             static terminal renderer for all 18 components
    widget/             interactive model over a surface (focus, edits,
                        button actions)
  agent/                orchestration: card resolution + version detect,
                        chat session, observed-task registry
    connv1/             v1.0 client over the official a2a-go SDK
  chat/                 transcript blocks, message splitting, sanitization
  compat03/             hand-rolled A2A 0.3 JSON-RPC + SSE client
  config/               saved agents + credentials (~/.config/a2a-tui)
  fixtureagent/         keyword-scripted fixture agent (served via a2a-go)
  md/                   glamour markdown rendering, per-block cache
  tui/                  the Bubble Tea app: panes, keys, slash commands
  wirelog/              raw HTTP frame capture (ring buffer)
scripts/smoke.sh        live-agent smoke test (make smoke)
docs/live-agents.md     verified public agents + drift notes
```

## License

Apache-2.0 — see [LICENSE](LICENSE).
