# agent-teams v2 control plane image. Build from the repository root:
#   docker build -f server/Dockerfile -t agent-teams-server .
# Stages: web app (Vue + Vite) → node daemon release tarball → atctl (Go) → runtime.

FROM node:22-bookworm-slim AS web
WORKDIR /src/web
# The /dev/office demo imports the default layout.
COPY office/default.json /src/office/default.json
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npx vue-tsc -b && npx vite build --outDir /out/web --emptyOutDir

# The daemon tarball served at /node/dist/agent-teams-node-<version>.tgz (architecture.md §14.3).
FROM node:22-bookworm-slim AS node
WORKDIR /src/node
COPY protocol/ /src/protocol/
COPY node/package.json node/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY node/ ./
RUN npm run pack && mkdir -p /out/node-dist && cp dist/agent-teams-node-*.tgz /out/node-dist/ \
 && cd /out/node-dist && for f in *.tgz; do sha256sum "$f" | cut -d' ' -f1 > "$f.sha256"; done

FROM golang:1.26-bookworm AS go
WORKDIR /src/server
ENV CGO_ENABLED=0 GOFLAGS=-trimpath
COPY protocol/gen/go/ /src/protocol/gen/go/
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
RUN go build -ldflags="-s -w" -o /out/atctl ./cmd/atctl

# juicefs formats and destroys the users' shared-folder volumes (AGENT_TEAMS_FS_*); pinned with its sha256.
FROM debian:bookworm-slim AS juicefs
ARG TARGETARCH=amd64
ARG JUICEFS_VERSION=1.4.2
ADD https://github.com/juicedata/juicefs/releases/download/v${JUICEFS_VERSION}/juicefs-${JUICEFS_VERSION}-linux-${TARGETARCH}.tar.gz /tmp/juicefs.tgz
RUN case "$TARGETARCH" in \
      amd64) sum=79648a0a67145bf42ca4e8336578c13a5bbc9a0bd34df340b3b93dac1e975247 ;; \
      arm64) sum=19f72a30d3642a2f7971c8c4a31d2647de62f19520716e85c3a777de5f4c9036 ;; \
      *) echo "no juicefs for $TARGETARCH" >&2; exit 1 ;; \
    esac \
 && echo "$sum  /tmp/juicefs.tgz" | sha256sum -c - \
 && tar -xzf /tmp/juicefs.tgz -C /usr/local/bin juicefs

FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates git tzdata \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --system --uid 65532 --home-dir /nonexistent --shell /usr/sbin/nologin atctl \
 && mkdir -p /var/lib/agent-teams/git-mirrors && chown -R atctl /var/lib/agent-teams
COPY --from=go /out/atctl /atctl
COPY --from=juicefs /usr/local/bin/juicefs /usr/local/bin/juicefs
COPY --from=web /out/web /srv/web
COPY --from=node /out/node-dist /srv/node-dist
COPY office/ /srv/office/
ENV PORT=3001 \
    WEB_DIR=/srv/web \
    OFFICE_DIR=/srv/office \
    NODE_DIST_DIR=/srv/node-dist \
    GIT_MIRROR_DIR=/var/lib/agent-teams/git-mirrors
USER atctl
EXPOSE 3001
HEALTHCHECK --interval=10s --timeout=3s --start-period=20s --retries=6 CMD ["/atctl", "healthcheck"]
ENTRYPOINT ["/atctl"]
# Needs DATABASE_URL, AGENT_TEAMS_PUBLIC_URL, AGENT_TEAMS_SECRET_KEY; optional VALKEY_URL,
# AGENT_TEAMS_ADMIN_EMAILS, BIND. Run "migrate up" (same image) before "serve".
CMD ["serve"]
