# syntax=docker/dockerfile:1

FROM golang:1.26-bookworm AS go-builder

WORKDIR /src

COPY server/go.mod server/go.sum ./
RUN go mod download

COPY server/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/agent-server ./cmd/server

FROM node:22-bookworm-slim AS runtime

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl sqlite3 \
    && rm -rf /var/lib/apt/lists/*

ARG INSTALL_CODEX=false
ARG INSTALL_CLAUDE=false

RUN npm install -g @google/gemini-cli \
    && if [ "$INSTALL_CODEX" = "true" ]; then npm install -g @openai/codex; fi \
    && if [ "$INSTALL_CLAUDE" = "true" ]; then npm install -g @anthropic-ai/claude-code; fi \
    && npm cache clean --force

RUN useradd -m -d /home/app -s /bin/bash app \
    && mkdir -p /app/data /home/app/.gemini /home/app/.codex /home/app/.claude \
    && chown -R app:app /app /home/app

COPY --from=go-builder --chown=app:app /out/agent-server /app/agent-server

USER app
WORKDIR /app

EXPOSE 8080

ENTRYPOINT ["/app/agent-server"]
