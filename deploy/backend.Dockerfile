# Multi-stage build for the backend.
#
# The upstream Dockerfile assumes bin/backend was cross-compiled beforehand
# (which is what CI does), and that would require a Go toolchain on the user's
# machine. This one compiles inside the container so Docker is enough.
#
# Neither sqlc nor mockery is needed: the generated code is committed, so
# go build alone is sufficient.

FROM golang:1.27-bookworm AS builder
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=local
RUN CGO_ENABLED=0 go build \
	-ldflags="-X 'main.Version=${VERSION}' -X 'main.BuildTime=$(date -u '+%FT%TZ')'" \
	-o /out/backend cmd/backend/main.go

FROM debian:bookworm-slim
WORKDIR /app

# The slim image ships no CA certificates. Without them the backend cannot
# reach https://oauth2.googleapis.com/token to exchange the access token and
# fails with
#   x509: certificate signed by unknown authority
# which surfaces as a 400 "invalid exchange token" on Google sign-in.
# Upstream uses the full golang image, which already carries them.
RUN apt-get update \
	&& apt-get install -y --no-install-recommends ca-certificates \
	&& rm -rf /var/lib/apt/lists/*

# Migrations run at startup from inside the backend, so they have to ship too.
COPY --from=builder /out/backend /app/backend
COPY --from=builder /src/internal/database/migrations /app/migrations

EXPOSE 8080
CMD ["/app/backend"]
