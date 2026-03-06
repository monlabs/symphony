# Symphony

Symphony is a long-running automation service that continuously reads work from an issue tracker (Linear), creates an isolated workspace for each issue, and runs a coding agent session for that issue inside the workspace.

## Features

- Polls Linear for issues in active states and dispatches coding agent sessions
- Per-issue workspace isolation with lifecycle hooks
- Multi-turn coding agent sessions via app-server protocol (JSON-RPC over stdio)
- Exponential retry with configurable backoff
- Reconciliation stops runs when issues change state
- Dynamic `WORKFLOW.md` reload without restart
- Optional HTTP server with dashboard and JSON API
- Structured logging with `slog`

## Setup

### Prerequisites

- Go 1.21+
- A Linear API key
- A coding agent that supports the app-server protocol (e.g., `codex app-server`)

### Build

```bash
make build
```

The binary is output to `bin/symphony`.

### Configuration

Create a `WORKFLOW.md` file with YAML front matter:

```markdown
---
tracker:
  kind: linear
  api_key: $LINEAR_API_KEY
  project_slug: my-project
  active_states:
    - Todo
    - In Progress
  terminal_states:
    - Done
    - Closed
    - Cancelled

polling:
  interval_ms: 30000

workspace:
  root: ~/symphony_workspaces

hooks:
  after_create: |
    git clone https://github.com/myorg/myrepo .
  before_run: |
    git pull origin main

agent:
  max_concurrent_agents: 5
  max_turns: 20

codex:
  command: codex app-server
  approval_policy: auto-edit
  turn_timeout_ms: 3600000

server:
  port: 8080
---

You are working on issue {{ issue.identifier }}: {{ issue.title }}

{{ issue.description }}

Please implement the required changes and create a pull request.
```

### Environment Variables

- `LINEAR_API_KEY` - Linear API token (can be referenced as `$LINEAR_API_KEY` in WORKFLOW.md)

## Usage

```bash
# Run with default WORKFLOW.md in current directory
./bin/symphony

# Run with explicit workflow file
./bin/symphony path/to/WORKFLOW.md

# Run with HTTP server on specific port
./bin/symphony --port 8080

# Run with both
./bin/symphony --port 8080 path/to/WORKFLOW.md
```

## HTTP API

When the HTTP server is enabled (via `--port` or `server.port` in WORKFLOW.md):

- `GET /` - Human-readable dashboard
- `GET /api/v1/state` - JSON system state snapshot
- `GET /api/v1/<issue_identifier>` - Issue-specific details
- `POST /api/v1/refresh` - Trigger immediate poll cycle

## Development

```bash
# Run tests
make test

# Run linters
make lint

# Build
make build

# Clean build artifacts
make clean
```

## Architecture

```
cmd/symphony/          CLI entry point
internal/
  config/              WORKFLOW.md loader and typed config
  domain/              Core domain models
  tracker/             Issue tracker client (Linear)
  workspace/           Workspace lifecycle management
  agent/               Coding agent subprocess protocol
  orchestrator/        Poll/dispatch/reconcile/retry loop
  server/              Optional HTTP server
```

## Specification

See [docs/SPEC.md](docs/SPEC.md) for the full service specification.
