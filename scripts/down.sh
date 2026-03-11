#!/usr/bin/env bash

if command -v docker-compose >/dev/null 2>&1; then
    DC="docker-compose"
else
    DC="docker compose"
fi

$DC down
