default: init run

init:
	test -f .env || cp .env.example .env
	grep -q '^REDIS_PASSWORD=' .env && sed -i "s/^REDIS_PASSWORD=$$/REDIS_PASSWORD=$$(openssl rand -hex 16)/" .env
	grep -q '^DB_PASS=' .env && sed -i "s/^DB_PASS=$$/DB_PASS=$$(openssl rand -hex 16)/" .env

DOCKER_COMPOSE := $(shell command -v docker-compose 2> /dev/null)

ifeq ($(DOCKER_COMPOSE),)
DC := docker compose
else
DC := docker-compose
endif

run:
	$(DC) up --build -d

down:
	$(DC) down