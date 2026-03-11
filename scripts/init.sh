#!/usr/bin/env bash

set -e

test -f .env || cp .env.example .env

grep -q '^REDIS_PASSWORD=' .env && \
sed -i "s/^REDIS_PASSWORD=$/REDIS_PASSWORD=$(openssl rand -hex 16)/" .env

grep -q '^DB_PASS=' .env && \
sed -i "s/^DB_PASS=$/DB_PASS=$(openssl rand -hex 16)/" .env

