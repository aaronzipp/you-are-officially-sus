# Multi-stage build for you-are-sus

# ---- Builder ----
FROM golang:1.25 AS builder
WORKDIR /src

# Pre-cache deps
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Copy source
COPY . .

# Build static binary
RUN --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/you-are-sus .

# ---- Runtime ----
FROM gcr.io/distroless/static:nonroot
WORKDIR /app

# Binary
COPY --from=builder /out/you-are-sus /app/you-are-sus

# App assets (templates, static files, data)
COPY --chown=nonroot:nonroot templates/ /app/templates/
COPY --chown=nonroot:nonroot static/ /app/static/
COPY --chown=nonroot:nonroot data/ /app/data/

EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/you-are-sus"]
