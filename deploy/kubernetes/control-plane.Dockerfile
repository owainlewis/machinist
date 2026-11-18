FROM --platform=$BUILDPLATFORM golang:1.26.6-bookworm AS build
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=development
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -trimpath -ldflags="-X main.version=${VERSION}" -o /machinist ./cmd/machinist

FROM debian:bookworm-slim
ARG TARGETARCH
ARG KUBECTL_VERSION=v1.36.4
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl git python3 procps \
    && rm -rf /var/lib/apt/lists/* \
    && curl -fsSLo /usr/local/bin/kubectl "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl" \
    && curl -fsSLo /tmp/kubectl.sha256 "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl.sha256" \
    && echo "$(cat /tmp/kubectl.sha256)  /usr/local/bin/kubectl" | sha256sum --check \
    && chmod 0755 /usr/local/bin/kubectl && rm /tmp/kubectl.sha256
ARG GH_VERSION=2.102.0
RUN cd /tmp \
    && curl -fsSLO "https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_linux_${TARGETARCH}.tar.gz" \
    && curl -fsSLo gh_checksums.txt "https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_checksums.txt" \
    && grep "  gh_${GH_VERSION}_linux_${TARGETARCH}.tar.gz$" gh_checksums.txt | sha256sum --check \
    && tar -xzf "gh_${GH_VERSION}_linux_${TARGETARCH}.tar.gz" \
    && install -m 0755 "gh_${GH_VERSION}_linux_${TARGETARCH}/bin/gh" /usr/local/bin/gh \
    && gh api --help | grep -q -- --slurp \
    && rm -rf gh_*
RUN command -v ps >/dev/null && ps axeww -o pid=,command= >/dev/null
COPY --from=build /machinist /usr/local/bin/machinist
ENV HOME=/var/lib/machinist/home GIT_TERMINAL_PROMPT=0
USER 1000:1000
WORKDIR /var/lib/machinist
ENTRYPOINT ["machinist"]
CMD ["start", "--config=/var/lib/machinist/config.yaml"]
