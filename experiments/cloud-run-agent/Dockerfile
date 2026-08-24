FROM node:22.22.2-bookworm-slim

ARG CODEX_VERSION=0.149.1

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        git \
    && rm -rf /var/lib/apt/lists/* \
    && npm install --global "@openai/codex@${CODEX_VERSION}"

RUN mkdir -p /workspace \
    && chown node:node /workspace

COPY --chown=node:node run-codex.sh /usr/local/bin/run-codex
RUN chmod 0755 /usr/local/bin/run-codex

USER node
WORKDIR /workspace

ENTRYPOINT ["/usr/local/bin/run-codex"]
