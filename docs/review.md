# Verification and independent review

Reviewed on 2026-11-13 by an independent coding agent that did not write the implementation.

## Findings resolved

1. GitHub issues with `body: null` could crash duplicate preparation. Normalize null bodies to empty text; regression test added.
2. Cancelled/expired drafts could permanently block a source message, and changed instructions reused an old preview. Allow replacement only before an external write, invalidate old confirmation tokens, and preserve created/creating/uncertain records; regression tests added.
3. A stale Telegram callback acknowledgment could discard confirmation after slow inference. Acknowledgment is now best effort and cannot suppress the durable confirmation path; regression test added.
4. Host configuration permissions did not match container UID/GID 10001. Setup now grants group read/traverse explicitly while retaining private `.env` permissions.

## Checks

- `python3 -m unittest discover -s tests -v`: 25 tests passed.
- `python3 -m compileall -q issue_pilot tests`: passed.
- Independent re-review confirmed the first three fixes and no identified P1 security finding in the reviewed scope.
- Container builds, real Codex subscription login/inference, and end-to-end Telegram/GitHub writes were not run in this workspace: Docker and deployment credentials are unavailable. CI includes both image builds when repository publication/access permits it.

This review does not establish production readiness or replace a deployment smoke test. Candidate-based duplicate search, sequential throughput, private draft retention and best-effort redaction are documented limitations, not guarantees.
