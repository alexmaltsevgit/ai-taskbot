# Go rewrite verification and independent review

The application was rewritten from Python to Go on 2026-11-13. All runtime application code is now Go, using only the standard library.

## Independent findings and fixes

An independent reviewer ran `go test -race -count=1 ./...` and `go vet ./...` and identified two P2 issues:

1. Repository matching truncated valid names ending with `-` or `.`. The parser now preserves the full token; a regression test covers two similarly named allowed repositories.
2. Cancelling the npm Codex launcher could leave its native child alive. Codex now runs in a separate Linux process group; cancellation kills the whole group. A real launcher/child regression test verifies no delayed child action survives timeout.

Both regression tests were observed failing before the fixes and passing afterward. No P1 was identified in the reviewed scope. Independent re-review of the fixes is pending.

## Verification

- `go test -race -count=1 ./...`: passed.
- `go vet ./...`: passed.
- `CGO_ENABLED=0 go build ./cmd/taskbot`: passed.
- `gofmt` applied to all Go source.
- Tests cover routing, access controls, preview/confirmation, duplicate and uncertain-write handling, durable state and locking, GitHub HTTP parsing/search, RS256 JWTs, worker authorization/bounds, and process cancellation.
- Container builds and CI results are tracked on the pull request.
- Real Codex login/inference and Telegram-to-GitHub end-to-end testing require server credentials and were not performed.

The README describes the state-format change and safe handling of any earlier SQLite deployment. No server deployment was performed.
