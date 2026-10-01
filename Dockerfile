# syntax=docker/dockerfile:1

# --- Dashboard ---------------------------------------------------------------
FROM node:22-alpine AS web
WORKDIR /src/web
RUN corepack enable
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY api/openapi.yaml /src/api/openapi.yaml
COPY web/ ./
RUN pnpm build

# --- Go binaries -------------------------------------------------------------
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/tanvir001728/hookyard/internal/version.Version=${VERSION} -X github.com/tanvir001728/hookyard/internal/version.Commit=${COMMIT} -X github.com/tanvir001728/hookyard/internal/version.Date=${DATE}" \
      -o /out/hookyard ./cmd/hookyard \
 && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/flakyvendor ./cmd/flakyvendor

# --- Runtime -----------------------------------------------------------------
# Distroless: no shell or package manager, CA certificates included, runs as
# a non-root user.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/hookyard /out/flakyvendor /usr/local/bin/
ENV HOOKYARD_ADDR=:8080 \
    HOOKYARD_LOG_FORMAT=json
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=4s --start-period=10s --retries=3 \
  CMD ["/usr/local/bin/hookyard", "healthcheck"]
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/hookyard"]
CMD ["serve"]

LABEL org.opencontainers.image.title="Hookyard" \
      org.opencontainers.image.description="Reliable delivery for outbound API calls" \
      org.opencontainers.image.source="https://github.com/tanvir001728/hookyard" \
      org.opencontainers.image.licenses="MIT"
