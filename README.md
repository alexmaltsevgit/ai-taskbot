# AI Taskbot

Turn a Telegram reply into a well-scoped GitHub issue. Mention the bot, let it match the project and look for duplicates, review the full draft, then confirm. Self-hosted; Go 1.24+, one static binary, no third-party Go dependencies. Model inference uses the official Codex CLI with your own eligible ChatGPT subscription.

```text
(reply to the bug report)
@your_bot создай задачу в my-account/project
```

`/issue@your_bot` as a reply also works. Without an explicit repository the bot uses a configured chat default, then the chat title and repository descriptions. Ambiguous choices produce a question rather than a write. Only configured chats, users and repositories are permitted.

## What is implemented

- Telegram mention/command + direct reply, including captions and correct UTF-16 entity handling.
- Live metadata for the repository allowlist; explicit project takes priority.
- Two-stage structured model output: routing, then draft and semantic duplicate assessment.
- GitHub lexical search plus 100 recently updated issues, including closed issues. Search is a **candidate search, not an exhaustive guarantee**; older semantically similar issues may be missed.
- Full preview with destination and public/private warning. Only the original requester, in the same chat, can confirm or cancel. All writes require this confirmation.
- Durable JSON state, a single-instance lock, source-level deduplication, expiring confirmations, and a durable pre-write state. A timed-out create is never blindly retried.
- GitHub App installation auth (preferred) or fine-grained PAT.
- Separate model worker with no GitHub/Telegram credentials, read-only container filesystem, ephemeral Codex runs, user config ignored, shell/apps/subagents disabled, and bounded input/output/time.
- Russian interaction messages; issue language follows the request. Prompts are in `internal/taskbot/prompts.go`.

## Setup

### 1. Telegram

Create a bot with **@BotFather**, keep the token secret and add the bot to the group. For arbitrary `@bot` mentions as replies to somebody else's message, disable group Privacy Mode through BotFather and re-add the bot if required. The safer alternative is the explicit `/issue@your_bot` command, which Telegram delivers in privacy mode. Admin privileges are not required.

When privacy mode is disabled, Telegram can deliver other group messages too; this application ignores messages that aren't addressed to it and does not store the full chat. Tell group participants how the bot works. Do not enable processing for a chat unless its members' project content may be sent to the model provider and the configured GitHub repositories.

### 2. GitHub

Create a GitHub App for the account, request **Metadata: read** and **Issues: read/write**, and install it only on the repositories you want. It needs no webhook, code write, administration, pull request, or organization permissions. Set `GITHUB_APP_ID` and `GITHUB_INSTALLATION_ID`; save its private key as `secrets/github-app.pem` readable by container UID 10001. Do not commit it.

For a simpler personal setup, set a fine-grained `GITHUB_TOKEN` with the same selected repositories and permissions. The token takes precedence when both methods are set.

### 3. Local configuration

```sh
cp .env.example .env
cp config.example.json config.json
mkdir -p secrets
chmod 600 .env
# Containers use UID/GID 10001. Grant read/traverse without making files public.
sudo chgrp 10001 config.json secrets
chmod 640 config.json
chmod 750 secrets
# After saving your GitHub App key (skip with PAT auth):
# sudo chgrp 10001 secrets/github-app.pem
# chmod 640 secrets/github-app.pem
openssl rand -hex 32
```

Put the generated value in `WORKER_TOKEN`, fill the Telegram and GitHub settings, and replace the example numeric IDs and repositories in `config.json`. Use immutable Telegram user IDs, not usernames. `default_repository` is optional. Multiple repositories per chat are supported. Adding a repository is an administrator action; model output cannot expand access.

### 4. Sign in using the subscription on the server

```sh
docker compose build
docker compose run --rm worker codex login --device-auth
```

Open the official login URL printed by Codex on your own browser and enter the one-time code. If required, enable device-code login in your ChatGPT security settings. Do not paste tokens, auth files or passwords into Telegram/GitHub. The auth volume survives container restarts.

```sh
docker compose run --rm worker codex login status
docker compose up -d
```

This uses the official CLI's saved authentication, not browser-cookie scraping or a reverse-engineered API. A qualifying plan, account/workspace permissions, supported region and sufficient usage allowance are still required. Subscription limits apply; there is no automatic paid API fallback. **The bot does not make a general public model endpoint**: only allowlisted users in your own work chats may request issue preparation. Don't turn one subscription into a resold/shared inference service. For hosted multi-customer use, review current OpenAI terms and official Sign in with ChatGPT options instead.

Official documentation: [headless authentication](https://learn.chatgpt.com/docs/auth), [non-interactive mode and saved auth](https://learn.chatgpt.com/docs/non-interactive-mode), [ChatGPT plan usage for open-source/self-hosted apps](https://developers.openai.com/siwc/token-sharing-open-source).

## Runtime and recovery

Only run one bot instance per state volume and Telegram token. Polling avoids inbound ports/certificates. Do not publish the worker's port or expose its token. Long inference requests run serially to limit subscription use. Telegram's pending-update retention is finite; this is a small-team service, not a high-throughput queue.

A draft lasts 24 hours. Repeat the original request if Telegram delivery fails: the same source returns its durable draft/result. After cancellation or expiry, repeat the original command to generate a fresh draft. A changed instruction by the original requester replaces an unsubmitted preview and invalidates its old buttons. Created or uncertain records are never replaced automatically.

If GitHub creation times out, the record becomes `uncertain` (or `creating` after a process crash). **Do not delete the record and immediately retry.** An administrator should inspect GitHub for the hidden `issue-pilot-source:<hash>` marker recorded in the JSON state file, including recently created issues. If found, record its URL and mark `created`; only reset to `preview` after confirming no issue was created. The app deliberately does not automate this uncertain financial-free but externally visible write.

Keep the state volume: losing it weakens duplicate protection. Back it up securely; drafts can contain private project information. Completed, cancelled and expired draft bodies are not automatically purged in this initial version; define your retention policy before deployment. Logs contain operation outcomes and update IDs, not raw messages/tokens.

## Limitations and trust boundaries

- Text and captions only. Images, documents, videos and voice are **not** downloaded or interpreted; the bot says when attachment details are missing.
- No automatic codebase inspection. It uses approved repository names/descriptions and issue candidates, not source code. It must not invent file paths or claim tests ran.
- Prompt-injection resistance relies on code allowlists, schemas, explicit confirmation and process isolation, not a promise that prompts are infallible.
- Secret-pattern redaction is best effort. Read the draft, especially for public repositories. The whole approved issue body is shown before creation.
- Docker isolation reduces blast radius; the worker still holds its own Codex credentials. Do not mount personal home folders or add plugins/MCP servers. Keep Codex and the host patched.
- The current machine's tests run without real Telegram/GitHub writes or model inference. Server credentials and a real end-to-end test are needed before calling the deployment operational.

## Development

```sh
go test -race ./...
go vet ./...
go build ./cmd/taskbot
```

No API keys or accounts are needed for tests. Linux is the deployment target (the instance lock uses flock). HTTP clients are isolated at the boundary; application tests exercise real parsing, durable state transitions and orchestration. CI also builds both container images.

See [architecture](docs/architecture.md) and [review](docs/review.md).

## Migration from the initial Python implementation

All application code is now Go; Python and OpenSSL signing subprocesses are removed. The Go standard library signs GitHub App JWTs. State is stored in `/data/state.json` using a 0600 temporary file, fsync, atomic rename and directory fsync. The creating barrier is persisted before any GitHub POST. The bot exits on a state-write failure rather than risking duplicate delivery.

The earlier SQLite file is **not automatically imported**. If the Python version was already deployed, stop it, back up its database and reconcile all pending/uncertain operations before starting this version. Do not run both implementations against the same Telegram bot. Starting with empty state loses the local deduplication history; GitHub candidate search alone is not an equivalent replacement. Keep the old database until migration/reconciliation is complete. The Codex auth volume and existing local config remain compatible.
