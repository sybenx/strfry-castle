# Builds the published ghcr.io/sybenx/castle-steward image (see PLAN.md
# Phase 7, DECISIONS.md). Two stages: compile the static steward binary,
# then lay it into a docker-cli-only base image.
#
# The final base is docker:cli, not scratch/alpine, because steward itself
# shells out to `docker exec <STRFRY_CONTAINER> strfry ...` (raid.go,
# stats.go) rather than talking to a Docker SDK — the docker.sock mount
# documented in CLAUDE.md/README is only useful if the `docker` client
# binary is present to speak to it. docker:cli ships that client and
# nothing else (no dockerd), which is the least code for this container's
# one job.
FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY steward/ steward/
ARG VERSION=dev
ARG TARGETOS=linux
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags "-s -w -X main.buildVersion=${VERSION}" -o /out/steward ./steward

FROM docker:28-cli
WORKDIR /app
COPY --from=builder /out/steward ./steward
COPY towncrier/ ./towncrier/
EXPOSE 8787
ENTRYPOINT ["./steward"]
