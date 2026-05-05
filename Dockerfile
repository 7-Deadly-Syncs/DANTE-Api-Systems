## syntax=docker/dockerfile:1.7

# ---------- Builder ----------
FROM golang:1.25-alpine AS builder

WORKDIR /app

ENV CGO_ENABLED=0

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -o /out/backend ./cmd/dante && \
    go build -trimpath -o /out/migrate ./cmd/migrate


# ---------- Runtime ----------
FROM alpine:3.19

WORKDIR /app

RUN apk add --no-cache ca-certificates wget

COPY --from=builder /out/backend /usr/local/bin/backend
COPY --from=builder /out/migrate /usr/local/bin/migrate
COPY --from=builder /app/db /app/db

EXPOSE 8080

CMD ["/usr/local/bin/backend"]
