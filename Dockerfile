FROM python:3.12-slim-bookworm AS bot
RUN apt-get update && apt-get install -y --no-install-recommends openssl ca-certificates && rm -rf /var/lib/apt/lists/*
RUN useradd --create-home --uid 10001 pilot && mkdir -p /app /data && chown pilot:pilot /data
WORKDIR /app
COPY issue_pilot ./issue_pilot
USER pilot
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 STATE_FILE=/data/state.sqlite CONFIG_FILE=/config/config.json
CMD ["python", "-m", "issue_pilot.bot"]

FROM node:22-bookworm-slim AS worker
ARG CODEX_VERSION=0.167.1
RUN apt-get update && apt-get install -y --no-install-recommends python3 ca-certificates && rm -rf /var/lib/apt/lists/* \
    && npm install -g @openai/codex@${CODEX_VERSION} \
    && codex exec --help | grep -q -- --ignore-user-config
RUN useradd --create-home --uid 10001 pilot && mkdir -p /home/pilot/.codex && chown -R pilot:pilot /home/pilot
WORKDIR /app
COPY issue_pilot ./issue_pilot
USER pilot
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 CODEX_HOME=/home/pilot/.codex
CMD ["python3", "-m", "issue_pilot.worker"]
