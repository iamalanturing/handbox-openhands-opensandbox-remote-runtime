# Contributing

`handbox` is an early, unreleased project — expect incomplete pieces and
breaking changes. Contributions and issue reports are still welcome.

## Development

```
go build -o handbox ./cmd/handbox
go build -o ai-level ./cmd/ai-level
go test ./...
```

Before opening a PR, make sure these all pass clean (CI runs the same
checks):

```
gofmt -l .
go vet ./...
go build ./...
go test ./...
```

## Pull requests

- Keep PRs scoped to one change; explain the "why" in the description, not
  just the "what."
- Add or update tests for behavior you change, especially in
  `internal/levels` (the level→policy mapping) and `internal/server`
  (the OpenHands↔OpenSandbox request/response translation) — both are
  meant to be fully testable without a live OpenSandbox instance.
- If you're touching the network-level policy logic in Section 6 of the
  implementation spec, double check the security requirement that the
  state file is never trusted from the incoming request and is read fresh
  on every `/start` call.
