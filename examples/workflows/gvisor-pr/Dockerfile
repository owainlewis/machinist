FROM golang:1.26.6-bookworm AS go
FROM node:22.22.2-bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates git gh python3 build-essential \
    && rm -rf /var/lib/apt/lists/*
ARG CODEX_VERSION=0.154.0
RUN npm install -g "@openai/codex@${CODEX_VERSION}"
COPY --from=go /usr/local/go /usr/local/go
ENV PATH="/usr/local/go/bin:${PATH}" \
    HOME=/work/home \
    CODEX_HOME=/work/home/.codex \
    GIT_TERMINAL_PROMPT=0 \
    GH_PROMPT_DISABLED=1 \
    PYTHONUNBUFFERED=1
COPY worker.py /opt/gvisor-pr/worker.py
USER 1000:1000
WORKDIR /work
ENTRYPOINT ["python3", "/opt/gvisor-pr/worker.py"]
