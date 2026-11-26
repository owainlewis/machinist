# Official lean Machinist control-plane image for Google Cloud Run and
# external-runner container deployments.
#
# Distinct from deploy/kubernetes/control-plane.Dockerfile (which bundles
# kubectl and procps for in-cluster Kubernetes runners), this image contains
# only ca-certificates, /usr/local/bin/gh, and /usr/local/bin/machinist.

FROM --platform=$BUILDPLATFORM golang:1.26.6-bookworm AS build
ARG TARGETARCH
ARG VERSION=development
ARG GH_VERSION=2.102.0

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN mkdir -p /out \
    && CGO_ENABLED=0 GOOS=linux GOARCH="${TARGETARCH}" \
       go build -trimpath -ldflags="-X main.version=${VERSION}" -o /out/machinist ./cmd/machinist

# Download and SHA-256 verify the GitHub CLI in the builder stage so curl and
# tar are not required in the final runtime image.
RUN cd /tmp \
    && curl -fsSLO "https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_linux_${TARGETARCH}.tar.gz" \
    && curl -fsSLo gh_checksums.txt "https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_checksums.txt" \
    && grep "  gh_${GH_VERSION}_linux_${TARGETARCH}.tar.gz$" gh_checksums.txt | sha256sum --check \
    && tar -xzf "gh_${GH_VERSION}_linux_${TARGETARCH}.tar.gz" \
    && install -m 0755 "gh_${GH_VERSION}_linux_${TARGETARCH}/bin/gh" /out/gh \
    && rm -rf gh_*

FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 1000 machinist \
    && useradd --uid 1000 --gid 1000 --no-create-home --shell /usr/sbin/nologin machinist \
    && install -d -o root -g root -m 0755 /srv/machinist

COPY --from=build /out/machinist /usr/local/bin/machinist
COPY --from=build /out/gh /usr/local/bin/gh

ENV HOME=/tmp/machinist-home \
    GH_CONFIG_DIR=/tmp/gh \
    GIT_TERMINAL_PROMPT=0

USER 1000:1000
WORKDIR /srv/machinist
EXPOSE 8080

ENTRYPOINT ["machinist"]
CMD ["start", "--config=/srv/machinist/config.yaml"]
