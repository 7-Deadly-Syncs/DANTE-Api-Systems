default: init run

-include .env
export

SQLC = go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0
MIGRATE = go run ./cmd/migrate

init:
	./scripts/init.sh

run:
	./scripts/run.sh

down:
	./scripts/down.sh

test:
	go test ./...

sqlc-generate:
	$(SQLC) generate

goose-up:
	$(MIGRATE) up

goose-down:
	$(MIGRATE) down

goose-status:
	$(MIGRATE) status

goose-create:
	go run github.com/pressly/goose/v3/cmd/goose@v3.26.0 -dir db/migrations create $(name) sql
