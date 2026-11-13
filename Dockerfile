FROM golang:1.24.13-bookworm AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/taskbot ./cmd/taskbot

FROM debian:bookworm-slim AS bot
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/* \
    && useradd --create-home --uid 10001 pilot && mkdir -p /data && chown pilot:pilot /data
COPY --from=build /out/taskbot /usr/local/bin/taskbot
USER pilot
ENV STATE_FILE=/data/state.json CONFIG_FILE=/config/config.json
ENTRYPOINT ["taskbot"]
CMD ["bot"]

FROM node:22-bookworm-slim AS worker
ARG CODEX_VERSION=0.167.1
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/* \
    && npm install -g @openai/codex@${CODEX_VERSION} \
    && codex exec --help | grep -q -- --ignore-user-config \
    && useradd --create-home --uid 10001 pilot && mkdir -p /home/pilot/.codex && chown -R pilot:pilot /home/pilot
COPY --from=build /out/taskbot /usr/local/bin/taskbot
USER pilot
ENV CODEX_HOME=/home/pilot/.codex
CMD ["taskbot", "worker"]
