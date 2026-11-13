FROM python:3.13-slim

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PIP_NO_CACHE_DIR=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1

# ssh: claude_code employees run on nodes over SSH.
RUN apt-get update \
    && apt-get install -y --no-install-recommends openssh-client \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY pyproject.toml README.md LICENSE ./
COPY src ./src
RUN pip install ".[server]"

# Default team; docker-compose mounts ./team.yaml over it.
COPY examples/company.yaml /app/team.yaml

RUN useradd --create-home --uid 1000 app
USER app

# Reads TEAM_FILE, HOST, PORT, AGENT_TEAMS_TOKEN, DATABASE_URL, VALKEY_URL, ANTHROPIC_API_KEY.
CMD ["agent-teams", "serve"]
