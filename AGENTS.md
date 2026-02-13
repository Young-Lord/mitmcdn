# AGENTS.md - MitmCDN

## Purpose
This file is for coding agents operating in this repository.
It captures the current build/test workflow and the code style used in the Go codebase.

## Project Snapshot
- Language: Go 1.24 (`go.mod`)
- Module: `mitmcdn`
- Entrypoint: `main.go`
- Runtime config: `config.toml` (create from `config.toml.example`)
- Database: SQLite (`mitmcdn.db`) through GORM
- Main behavior: MITM proxy + aggressive CDN file caching
- Protocols: HTTP proxy, HTTPS MITM/CONNECT, SOCKS5, URL-path reverse proxy

## Key Paths
```text
main.go
integration_test.go
config.toml.example
src/config/config.go
src/cache/manager.go
src/download/scheduler.go
src/database/models.go
src/proxy/mitm.go
src/proxy/unified.go
src/proxy/status.go
docs/TESTING.md
docs/INTEGRATION_TESTING.md
```

## Build and Run
```bash
go mod download
go build -o mitmcdn .
go run . -config config.toml -db mitmcdn.db
./mitmcdn -config config.toml -db mitmcdn.db
```

## Format and Lint
No `golangci-lint` config is currently present. Use standard Go tools.

```bash
gofmt -l .
gofmt -w .
go vet ./...
staticcheck ./...   # optional, only if installed
```

## Test Commands
```bash
# all tests
go test ./...
go test ./... -v
# package-level tests
go test ./src/config -v
go test ./src/cache -v
go test ./src/database -v
go test ./src/download -v
go test ./src/proxy -v
# root package integration tests
go test . -v
# quality runs
go test ./... -race
go test ./... -cover
go test ./... -coverprofile=coverage.out
go tool cover -html=coverage.out
```

## Running a Single Test (Important)
Use anchored regex (`^...$`) with `-run` to avoid partial matches.

```bash
# one unit test
go test ./src/cache -run '^TestComputeFileHash$' -v
# one config test
go test ./src/config -run '^TestLoadConfigDefaults$' -v
# one integration test from root package
go test . -run '^TestHTTPProxy$' -v -timeout 60s
# long-running integration test
go test . -run '^TestConcurrentRequests$' -v -timeout 120s
# selected integration subset
go test . -run '^(TestHTTPProxy|TestSOCKS5Proxy)$' -v -timeout 120s
```

Agent notes:
- Integration tests depend on live network targets (for example `httpbin.org`).
- Network-related flakes are possible; rerun before changing logic.
- During iteration, run narrow tests first, then `go test ./...`.

## Code Style Guidelines
### Formatting
- Follow `gofmt` output exactly.
- Keep code idiomatic; do not manually align spacing.
- Let `gofmt` manage indentation and wrapping.
### Imports
- Import grouping when multiple groups exist:
  1) standard library
  2) internal module imports (`mitmcdn/src/...`)
  3) third-party imports (`gorm.io/...`, `golang.org/x/...`, etc.)
- Separate groups with one blank line.
### Naming
- Exported: PascalCase (`NewScheduler`, `GenerateCertificate`).
- Unexported: camelCase (`downloadTask`, `handleDownloadError`).
- Keep common acronyms uppercase in exported names (`URL`, `HTTP`, `SOCKS5`, `CDN`, `TTL`).
- Constructors usually follow `NewXxx(...) (*Xxx, error)`.
### Types and Struct Tags
- Config structs use TOML tags (`toml:"..."`).
- Database models use GORM tags (`gorm:"..."`).
- Keep status values as existing string states:
  `pending`, `downloading`, `paused`, `complete`, `failed`.
- Prefer extending existing structs over introducing parallel variants.
### Error Handling and Logging
- Wrap errors with context using `%w` when returning (`fmt.Errorf("...: %w", err)`).
- Startup failures in `main.go` use `log.Fatalf`.
- Runtime failures generally use `log.Printf` and continue when safe.
- Proxy/download paths may include stack traces (`runtime/debug.Stack()`).
- Some runtime failures are persisted to `database.Log`.
### Concurrency
- Shared maps/state use `sync.RWMutex` or `sync.Mutex`.
- Keep lock scope small and avoid lock + blocking I/O combinations.
- Existing logic uses channels for pause/resume and stream fan-out.
- Non-blocking sends are used in hot paths:
  `select { case ch <- v: default: }`
### HTTP and Proxy Behavior
- Preserve protocol detection flow in `src/proxy/unified.go`.
- Keep URL-path proxy behavior:
  `/https://target.example/path` and `/http://target.example/path`.
- Preserve status endpoints:
  `/api/status` (JSON) and `/status` (HTML).
### Testing Style
- Unit tests are colocated as `*_test.go` files.
- Integration tests are in `integration_test.go` at repo root.
- Prefer table-driven tests with `t.Run(...)`.
- Use `t.TempDir()` and `t.Cleanup(...)` for isolation.
- Use standard `testing` assertions (`t.Fatal`, `t.Fatalf`, `t.Error`, `t.Errorf`).
## Agent Workflow Conventions
- Make focused changes; avoid unrelated refactors.
- Do not commit runtime artifacts (`mitmcdn`, `*.db`, `data/`, `config.toml`, `mitmcdn.log`).
- If behavior changes, update docs in `docs/` when requested.
- Prefer targeted verification first, then broader validation.
## Cursor and Copilot Rules
Checked in this repository:
- `.cursor/rules/`: not present
- `.cursorrules`: not present
- `.github/copilot-instructions.md`: not present
If these files are added later, treat them as higher-priority repository instructions.
