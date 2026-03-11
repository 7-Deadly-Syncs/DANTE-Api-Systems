# ---------- Builder ----------
FROM golang:1.25-alpine AS builder

WORKDIR /app

ENV CGO_ENABLED=0

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN go build -o backend ./cmd/dante


# ---------- Runtime ----------
FROM alpine:3.19

WORKDIR /app

COPY --from=builder /app/backend /usr/local/bin/backend

EXPOSE 8080

CMD ["/usr/local/bin/backend"]