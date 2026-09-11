# a2a-tui

A Claude-Code-style terminal client for agents speaking the
[A2A protocol](https://a2a-protocol.org) (v1.0) and rendering
[A2UI](https://a2ui.org) surfaces — built in Go with
[Bubble Tea](https://github.com/charmbracelet/bubbletea).

`a2a-tui` is both a **user client** (chat with any A2A agent, watch tasks
stream live) and a **testing tool** for the protocol itself (raw JSON-RPC
console, wire logging, agent-card inspection, protocol-version negotiation).

## Features

- **A2A v1.0** — agent card discovery (`/.well-known/agent-card.json`),
  blocking + streaming sends (`SendMessage` / `SendStreamingMessage` SSE),
  task lifecycle (`GetTask`, `ListTasks`, `CancelTask`, `SubscribeToTask`
  with reconnect), push-notification webhooks via a built-in local listener.
- **A2A v0.3 compat** — the public ecosystem still has many 0.3 agents
  (including most streaming ones); a2a-tui auto-detects the protocol version
  from the agent card and speaks either dialect.
- **A2UI in the terminal** — the 18 canonical A2UI components rendered as
  terminal UI, with interactive inputs (forms, sliders, pickers) whose
  actions flow back to the agent. First terminal renderer for A2UI.
- **Testing tooling** — raw JSON-RPC console for arbitrary methods, raw
  request/response wire logging, A2A error codes surfaced by name, protocol
  version visibility (`A2A-Version` header).

## Status

Early development. See the [issue tracker](https://github.com/HenryNebula/a2a-tui/issues)
for the milestone plan (M1 scaffold → M9 v0.1.0 release).

## Install / run

Requires Go 1.25+.

```sh
go install github.com/HenryNebula/a2a-tui/cmd/a2a-tui@latest
a2a-tui --agent https://example-agent.example
```

Or from a checkout:

```sh
make run    # go run ./cmd/a2a-tui
make test   # unit tests
make ci     # gofmt + vet + test -race (what CI runs)
```

## Keys

| Key | Action |
| --- | --- |
| `enter` | send message / run slash command |
| `alt+enter`, `ctrl+j` | newline in the input |
| `?` | help |
| `ctrl+c` / `ctrl+d` | quit |

Slash commands (growing): `/help`, `/connect <url>`, `/quit`.

## Configuration

Per-user config and saved credentials live under `~/.config/a2a-tui/` and are
never read from (or written to) the repository.

## License

Apache-2.0 — see [LICENSE](LICENSE).
