# syntax=docker/dockerfile:1
# agent-teams node image: the node daemon (agent-teams-node) plus the CLIs its employees use
# (Claude Code, Codex, git, ssh, gh, tini) and geesefs + fuse3 for the shared folder. Build from the
# repository root:
#   docker build -f deploy/node/Dockerfile -t agent-teams-node .
# Runs as the non-root user "agent" (uid 1000): bypassPermissions refuses to run as root.
# See deploy/node/README.md for auth, volumes and the compose example.

# Full (not -slim) Debian image on purpose: git, curl, openssh-client, python3, make and gcc are
# already there, which employees need to build and test most projects.
ARG NODE_IMAGE=node:22-bookworm

# ── daemon release: same tarball the control plane serves to install-node.sh ──
FROM ${NODE_IMAGE} AS build
WORKDIR /src/node
COPY protocol/ /src/protocol/
COPY node/package.json node/package-lock.json ./
RUN --mount=type=secret,id=build_ca,required=false \
    if [ -s /run/secrets/build_ca ]; then export NODE_EXTRA_CA_CERTS=/run/secrets/build_ca; fi; \
    npm ci --no-audit --no-fund --loglevel=error
COPY node/ ./
RUN --mount=type=secret,id=build_ca,required=false \
    if [ -s /run/secrets/build_ca ]; then export NODE_EXTRA_CA_CERTS=/run/secrets/build_ca; fi; \
    npm run pack \
 && mkdir -p /out/agent-teams-node \
 && tar -xzf dist/agent-teams-node-*.tgz -C /out/agent-teams-node --strip-components=1 \
 && cd /out/agent-teams-node && npm ci --omit=dev --no-audit --no-fund --loglevel=error \
 && rm -rf node_modules/@anthropic-ai/claude-agent-sdk-linux-*-musl
# (the runtime is glibc Debian: the musl build of the bundled Claude Code is dead weight, ~340 MB)

# ── runtime ──
FROM ${NODE_IMAGE}
# Network steps below accept an optional BuildKit secret "build_ca" (the CA of a TLS-intercepting
# proxy, e.g. --secret id=build_ca,src=ca.crt); it is mounted per step and never stored in the image.
# Same gh release the daemon would otherwise download on first use (node/src/git/gh-install.ts).
ARG GH_VERSION=2.103.3
ARG TINI_VERSION=0.19.0
# Same geesefs release and checksums the console sends in fs.mount (server/internal/userfs).
ARG GEESEFS_VERSION=0.43.9
ARG GEESEFS_SHA256_AMD64=8253f0bad8070f504b3ca94a76919d9617252e7ffae04efca9ca54d5cfbaaca8
ARG GEESEFS_SHA256_ARM64=d2a47ea0833c2369e1f7ba92cada1c8caadd0e91144f4219fd0ce6a483e52461
ARG TARGETARCH

# fuse3: fusermount3 (setuid) lets the daemon, as uid 1000, mount the shared folder with geesefs.
# The container needs /dev/fuse, cap_add SYS_ADMIN and apparmor:unconfined for that (compose.yml).
RUN apt-get update && apt-get install -y --no-install-recommends fuse3 && rm -rf /var/lib/apt/lists/*

RUN --mount=type=secret,id=build_ca,required=false \
    set -eu; ca=""; [ -s /run/secrets/build_ca ] && ca="--cacert /run/secrets/build_ca"; \
    arch="${TARGETARCH:-$(dpkg --print-architecture)}"; cd /tmp; \
    tini="https://github.com/krallin/tini/releases/download/v${TINI_VERSION}/tini-${arch}"; \
    curl -fsSL $ca -o "tini-${arch}" "$tini"; curl -fsSL $ca "$tini.sha256sum" | sha256sum -c --quiet -; \
    install -m 0755 "tini-${arch}" /usr/local/bin/tini; rm -f "tini-${arch}"; \
    base="https://github.com/cli/cli/releases/download/v${GH_VERSION}"; \
    file="gh_${GH_VERSION}_linux_${arch}.tar.gz"; \
    curl -fsSL $ca -o "$file" "$base/$file"; \
    curl -fsSL $ca "$base/gh_${GH_VERSION}_checksums.txt" | grep " $file\$" | sha256sum -c --quiet -; \
    tar -xzf "$file"; install -m 0755 "gh_${GH_VERSION}_linux_${arch}/bin/gh" /usr/local/bin/gh; \
    rm -rf /tmp/gh_*; \
    geesefs="geesefs-linux-${arch}"; \
    sum="$GEESEFS_SHA256_AMD64"; [ "$arch" = arm64 ] && sum="$GEESEFS_SHA256_ARM64"; \
    curl -fsSL $ca -o "$geesefs" "https://github.com/yandex-cloud/geesefs/releases/download/v${GEESEFS_VERSION}/$geesefs"; \
    echo "$sum  $geesefs" | sha256sum -c --quiet -; \
    install -D -m 0755 "$geesefs" "/opt/geesefs/geesefs-${GEESEFS_VERSION}"; rm -f "$geesefs"

COPY --from=build /out/agent-teams-node /opt/agent-teams-node
COPY deploy/node/entrypoint.sh /usr/local/bin/agent-teams-entrypoint
# /opt/agent-teams-node is the image's release: the launcher seeds it into the data volume
# (~/.local/share/agent-teams/releases/) and runs current/node.mjs from there, so the console's
# "更新" button can switch releases without a new image (architecture.md §14.3).
# claude / codex on PATH are the exact CLIs the running release drives (the Agent SDK's bundled
# Claude Code and the daemon's own @openai/codex dependency), so `claude login` / `codex login` match them.
ARG DATA=/home/agent/.local/share/agent-teams
RUN sdk="$(basename /opt/agent-teams-node/node_modules/@anthropic-ai/claude-agent-sdk-linux-*)" \
 && ln -s "$DATA/current/node_modules/@anthropic-ai/$sdk/claude" /usr/local/bin/claude \
 && ln -s "$DATA/current/node_modules/.bin/codex" /usr/local/bin/codex \
 && printf '#!/bin/sh\nexec node %s/current/node.mjs "$@"\n' "$DATA" > /usr/local/bin/agent-teams-node \
 && chmod 0755 /usr/local/bin/agent-teams-node /usr/local/bin/agent-teams-entrypoint \
 && groupmod -n agent node && usermod -l agent -d /home/agent -m node \
 && install -d -o agent -g agent -m 0700 \
      /home/agent/.claude /home/agent/.codex /home/agent/.agents_team /home/agent/.ssh \
      /home/agent/.config /home/agent/.config/agent-teams \
      /home/agent/.local /home/agent/.local/state /home/agent/.local/state/agent-teams \
      /home/agent/.local/share /home/agent/.local/share/agent-teams \
 && install -d -o agent -g agent -m 0755 /home/agent/.fs

USER agent
WORKDIR /home/agent
# CLAUDE_CONFIG_DIR keeps `claude login` and the daemon's sessions on the same ~/.claude volume
# (otherwise Claude Code writes ~/.claude.json outside it).
ENV HOME=/home/agent \
    CLAUDE_CONFIG_DIR=/home/agent/.claude \
    CODEX_HOME=/home/agent/.codex \
    AGENT_TEAMS_ROOT=/home/agent/.agents_team \
    AGENT_TEAMS_STATE=/home/agent/.local/state/agent-teams \
    AGENT_TEAMS_SEED=/opt/agent-teams-node \
    AGENT_TEAMS_FS_MOUNT_DIR=/home/agent/.fs \
    DISABLE_AUTOUPDATER=1 \
    LANG=C.UTF-8
VOLUME ["/home/agent/.claude", "/home/agent/.codex", "/home/agent/.agents_team", "/home/agent/.local/state/agent-teams", "/home/agent/.local/share/agent-teams"]
ENTRYPOINT ["/usr/local/bin/tini", "--", "/usr/local/bin/agent-teams-entrypoint"]
# Needs AGENT_TEAMS_CONSOLE and AGENT_TEAMS_NODE_TOKEN. Any other command (claude login,
# codex login, bash) runs instead of the daemon.
CMD ["run"]
