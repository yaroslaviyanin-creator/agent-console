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

RUN if [ "$INSTALL_CODEX" = "true" ]; then npm install -g @openai/codex; fi \
    && if [ "$INSTALL_CLAUDE" = "true" ]; then npm install -g @anthropic-ai/claude-code; fi \
    && if [ "$INSTALL_CODEX" = "true" ] || [ "$INSTALL_CLAUDE" = "true" ]; then npm cache clean --force; fi

RUN useradd -m -d /home/app -s /bin/bash app \
    && mkdir -p /app/data /home/app/.local/bin /home/app/.local/share/agunlocker /home/app/.gemini /home/app/.codex /home/app/.claude \
    && chown -R app:app /app /home/app

# Install Linux x86_64 Antigravity Unlocker binary (amd64 only).
RUN ARCH="$(uname -m)" \
    && case "$ARCH" in \
        x86_64|amd64) ;; \
        *) echo "Unsupported architecture for ag_unlocker: $ARCH (only linux/amd64 is supported)" >&2; exit 1 ;; \
    esac \
    && TAG="$(curl -fsSLo /dev/null -w '%{url_effective}' 'https://github.com/confeden/Antigravity/releases/latest')" \
    && TAG="${TAG##*/}" \
    && VER="${TAG#v}" \
    && ALT="$(echo "$VER" | sed -E 's/\.([0-9]+)$/_\1/')" \
    && TMP_DIR="$(mktemp -d)" \
    && DOWNLOADED="" \
    && for cand in "$VER" "$ALT"; do \
        URL="https://github.com/confeden/Antigravity/releases/download/${TAG}/AG_${cand}_linux.tar.gz"; \
        if curl -fsSL "$URL" -o "$TMP_DIR/ag.tar.gz" 2>/dev/null; then \
            tar -xzf "$TMP_DIR/ag.tar.gz" -C "$TMP_DIR"; \
            install -m 0755 "$TMP_DIR/AG_${cand}_linux/ag_unlocker" /usr/local/bin/ag_unlocker; \
            DOWNLOADED="1"; \
            break; \
        fi; \
    done \
    && rm -rf "$TMP_DIR" \
    && test -n "$DOWNLOADED"

COPY --from=go-builder --chown=app:app /out/agent-server /app/agent-server
COPY --chown=app:app --chmod=0755 deploy/entrypoint.sh /app/entrypoint.sh

USER app
WORKDIR /app

ENV PATH="/home/app/.local/bin:${PATH}"

RUN curl -fsSL https://antigravity.google/cli/install.sh | bash

EXPOSE 8080

ENTRYPOINT ["/app/entrypoint.sh"]
