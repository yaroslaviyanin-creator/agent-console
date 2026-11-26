# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS go-source
WORKDIR /src
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./

FROM go-source AS build-core
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/agent-server ./cmd/server

FROM go-source AS build-runner
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/agent-runner ./cmd/runner

FROM debian:bookworm-slim AS core
RUN apt-get update \
    && apt-get install -y --no-install-recommends sqlite3 \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd -g 1001 app \
    && useradd -u 1001 -g app -m -d /home/app -s /usr/sbin/nologin app \
    && install -d -o app -g app -m 0700 /app/data
COPY --from=build-core /out/agent-server /app/agent-server
ENV RUNNING_IN_DOCKER=true
USER app
WORKDIR /app
EXPOSE 8080
ENTRYPOINT ["/app/agent-server"]

FROM node:22-bookworm-slim AS runner
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/* \
    && useradd -m -d /home/app -s /bin/bash app \
    && install -d -m 0555 /workspace \
    && install -d -o app -g app -m 0700 /home/app/.gemini /home/app/.codex /home/app/.codex-chat /home/app/.claude
RUN npm install -g @openai/codex@0.172.0 @anthropic-ai/claude-code@2.1.332 \
    && npm cache clean --force
RUN mkdir -p /tmp/agy-install \
    && curl -fsSL https://antigravity.google/cli/install.sh -o /tmp/agy-install/install.sh \
    && HOME=/tmp/agy-install bash /tmp/agy-install/install.sh \
    && install -m 0755 /tmp/agy-install/.local/bin/agy /usr/local/bin/agy \
    && rm -rf /tmp/agy-install
ARG AGY_USE_UNLOCKER=false
RUN --mount=type=bind,source=deploy/patch-agy.cjs,target=/tmp/patch-agy.cjs \
    if [ "$AGY_USE_UNLOCKER" = "true" ]; then node /tmp/patch-agy.cjs /usr/local/bin/agy; fi
COPY --from=build-runner /out/agent-runner /app/agent-runner
ENV RUNNING_IN_DOCKER=true HOME=/home/app
USER app
WORKDIR /app
EXPOSE 8081
ENTRYPOINT ["/app/agent-runner"]

FROM node:22-bookworm-slim AS unlocker
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl socat \
    && rm -rf /var/lib/apt/lists/* \
    && useradd -m -d /home/app -s /usr/sbin/nologin app \
    && install -d -o app -g app -m 0700 /home/app/.local/share/agunlocker
ARG AG_UNLOCKER_VERSION=v2.35.0
RUN set -eu; \
    tag="$AG_UNLOCKER_VERSION"; \
    version="${tag#v}"; \
    alt="$(printf '%s' "$version" | sed -E 's/\.([0-9]+)$/_\1/')"; \
    temp="$(mktemp -d)"; \
    for candidate in "$version" "$alt"; do \
        if curl -fsSL "https://github.com/confeden/Antigravity/releases/download/$tag/AG_${candidate}_linux.tar.gz" -o "$temp/ag.tar.gz"; then \
            tar -xzf "$temp/ag.tar.gz" -C "$temp"; \
            install -m 0755 "$temp/AG_${candidate}_linux/ag_unlocker" /usr/local/bin/ag_unlocker; \
            break; \
        fi; \
    done; \
    test -x /usr/local/bin/ag_unlocker; \
    rm -rf "$temp"
COPY --chmod=0755 deploy/unlocker-entrypoint.sh /usr/local/bin/unlocker-entrypoint
ENV HOME=/home/app
USER app
EXPOSE 53130
ENTRYPOINT ["/usr/local/bin/unlocker-entrypoint"]
