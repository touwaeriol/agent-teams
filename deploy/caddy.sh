#!/usr/bin/env bash
# Serve the platform at https://DOMAIN through the host's Caddy (automatic HTTPS).
# Writes one site file and imports it from the main Caddyfile, then reloads Caddy.
#   sudo deploy/caddy.sh agent.example.com 3001
set -euo pipefail

domain="${1:?usage: caddy.sh DOMAIN PORT}"
port="${2:?usage: caddy.sh DOMAIN PORT}"
caddyfile="${CADDYFILE:-/etc/caddy/Caddyfile}"
site="$(dirname "$caddyfile")/agent-teams.caddy"

command -v caddy > /dev/null || { echo "没有找到 caddy，请先安装：https://caddyserver.com/docs/install" >&2; exit 1; }
[ -f "$caddyfile" ] || { echo "没有找到 $caddyfile（用 CADDYFILE=... 指定）" >&2; exit 1; }

# Warn early if DNS does not point here yet: Caddy could not get a certificate.
resolved="$(getent ahostsv4 "$domain" 2> /dev/null | awk 'NR==1 {print $1}' || true)"
if [ -z "$resolved" ] || ! hostname -I | tr ' ' '\n' | grep -qxF "$resolved"; then
  echo "提示：$domain 解析到 ${resolved:-（无）}，不是本机地址（$(hostname -I)）。" >&2
  echo "      如果有 NAT 或 CDN 可以忽略；否则 Caddy 申请证书会失败。" >&2
fi

cat > "$site" <<CADDY
# agent-teams: managed by deploy/caddy.sh
$domain {
	reverse_proxy 127.0.0.1:$port {
		# Live updates (SSE) must not be buffered; WebSockets are proxied automatically.
		flush_interval -1
	}
}
CADDY

grep -qxF "import $site" "$caddyfile" || printf '\nimport %s\n' "$site" >> "$caddyfile"
caddy validate --config "$caddyfile" --adapter caddyfile > /dev/null
if command -v systemctl > /dev/null && systemctl is-active --quiet caddy 2> /dev/null; then
  systemctl reload caddy
else
  caddy reload --config "$caddyfile" --adapter caddyfile
fi
echo "Caddy 已配置：https://$domain -> 127.0.0.1:$port"
