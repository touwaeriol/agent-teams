# The web app (web/) is built first and copied into the Python package,
# which serves it at / (see server/app.py).
FROM node:22-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npx tsc -b && npx vite build --outDir /web/dist --emptyOutDir

FROM python:3.13-slim

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PIP_NO_CACHE_DIR=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1

WORKDIR /app
COPY pyproject.toml README.md LICENSE ./
COPY src ./src
COPY --from=web /web/dist ./src/agent_teams/server/static/app
RUN pip install ".[server]"

RUN useradd --create-home --uid 1000 app
USER app

# Reads HOST, PORT, AGENT_TEAMS_PUBLIC_URL, DATABASE_URL, VALKEY_URL,
# AGENT_TEAMS_SECRET_KEY (encrypts saved Git tokens), AGENT_TEAMS_ADMIN_EMAILS.
CMD ["agent-teams", "serve"]
