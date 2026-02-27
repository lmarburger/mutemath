# mutemath

GitHub notification spam cleaner. Identifies team-only review request notifications and mutes them, keeping your inbox useful.

## Build, Test, Run

```
go build -o mutemath .
go test -v ./...
go vet ./...

GH_TOKEN=ghp_... ./mutemath --verbose            # dry-run
GH_TOKEN=ghp_... ./mutemath --apply --daemon      # long-running mode
```

## Architecture: Functional Core, Imperative Shell

This codebase follows FCIS. The `core/` package contains pure decision logic. The root package (`main.go`, `github.go`) is the imperative shell handling I/O and orchestration.

### Core rules (`core/`)

- **Purity**: every function is deterministic — same inputs, same outputs. No side effects.
- **No hidden I/O**: no network calls, file access, env vars, `time.Now()`, or random. Pass these as arguments from the shell.
- **Immutability by default**: functions return new values, never mutate inputs.
- **Data-in/data-out APIs**: accept simple values/types, return explicit results (decisions, formatted strings). No framework types.
- **The core must not import the shell** — Go enforces this structurally since `core/` cannot import `main`.

### Shell rules (`main.go`, `github.go`)

- **Keep the shell thin**: minimal branching. Linear flow: fetch data → call core → execute effects → format output.
- **All I/O lives here**: HTTP calls, env vars, signal handling, logging, printing.
- **Translate at the boundary**: JSON/API types are internal to `github.go`. The shell converts them to core types before passing to the core.
- **No business logic in the shell**: if you're writing an `if` that decides what *action* to take, it belongs in the core.

### Testing rules

- **Core gets many fast unit tests**: no mocks, no HTTP, just input/output assertions.
- **Shell gets few (or zero) integration tests**: verify wiring against real APIs manually.
- **If tests need deep mocks, it's a design smell**: push more logic into the core.

### Common mistakes to avoid

- Don't let `net/http`, `encoding/json`, or GitHub API types leak into `core/`.
- Don't add extra service/manager layers — just core + thin shell.
- Don't mock your way out of poor separation. If it's hard to test, move logic to the core.

## Project Conventions

- **Standard library only** — no third-party dependencies.
- **Single binary**: `mutemath`.
- **Error handling**: return errors, don't panic. Shell logs errors and continues in daemon mode.
- **Naming**: Go conventions — exported types/functions in `core/` are the public API. Shell types are unexported where possible.
