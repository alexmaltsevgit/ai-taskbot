# Architecture and decisions

The owner requested a public, self-hosted Telegram bot that converts a replied message to GitHub issues, infers the project from explicit names or chat titles, searches duplicates, and uses subscription-based model authentication.

The implementation uses a deterministic Go control plane and an isolated Codex CLI worker. A direct API-key service would be simpler but does not meet the subscription preference. A model with direct GitHub tools would be more flexible but would unnecessarily combine untrusted chat content and repository write authority. Here the model only returns a proposal.

Flow: authorized mention/reply → source extraction/redaction → allowed repository metadata → routing proposal → duplicate candidates → final proposal → durable atomic JSON draft → full Telegram preview → requester confirmation → visibility/marker recheck → atomic creating state → one GitHub POST → durable result.

The public repository contains only generic code and synthetic examples. Production repository names, group IDs, credentials and message content belong in local configuration/state. No external account or deployment is provisioned by the source code.

Non-goals: autonomous issue editing/closure, customer-facing inference hosting, voice/image recognition, source-code access, deployment to a user's server without credentials, and guaranteed global semantic duplicate recall.

The Go rewrite uses only the standard library, including RSA signing for GitHub App tokens. JSON snapshots replace the former SQLite backend without changing the write-ahead creating barrier. Linux file locking prevents two pollers sharing state.
