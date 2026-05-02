# ---------- Builder ----------
FROM golang:1.25-alpine AS builder

WORKDIR /app

ENV CGO_ENABLED=0

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN go build -o backend ./cmd/dante
RUN go build -o migrate ./cmd/migrate


# ---------- Runtime ----------
FROM alpine:3.19

WORKDIR /app

RUN apk add --no-cache ca-certificates wget

COPY --from=builder /app/backend /usr/local/bin/backend
COPY --from=builder /app/migrate /usr/local/bin/migrate
COPY --from=builder /app/db /app/db

EXPOSE 8080

CMD ["/usr/local/bin/backend"]
