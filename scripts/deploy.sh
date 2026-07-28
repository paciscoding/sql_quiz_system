#!/usr/bin/env bash
#
# Manual deploy for the on-prem server.
#
# CI (.github/workflows/build.yml) builds and pushes the images to GHCR but cannot
# deploy: the server is behind NAT, so GitHub-hosted runners can't reach it. Run this
# on the server instead, from the directory holding docker-compose.yml and .env.
#
#   ./scripts/deploy.sh              # deploy :latest
#   ./scripts/deploy.sh <commit-sha> # deploy an exact build (also how you roll back)
#
# Requires: a `docker login ghcr.io` with read:packages, since the packages are private.

set -euo pipefail

IMAGE_TAG="${1:-latest}"
export IMAGE_TAG

cd "$(dirname "$0")/.."

if [ ! -f .env ]; then
  echo "error: no .env in $(pwd) — deploying would start the stack with an empty config" >&2
  exit 1
fi

# nginx serves TLS from these files; if they're missing the frontend container
# crash-loops on boot and the deploy presents as broken rather than "no cert".
if [ ! -f /opt/nginx/certs/cert.pem ] || [ ! -f /opt/nginx/certs/key.pem ]; then
  echo "error: TLS cert missing at /opt/nginx/certs/{cert,key}.pem" >&2
  echo "       generate it on this host first: ./scripts/gen-selfsigned-cert.sh <server-ip>" >&2
  exit 1
fi

echo ">> deploying tag: $IMAGE_TAG"

# Fails early with a clear registry error if the login/PAT lacks read:packages,
# rather than half-restarting the stack.
docker compose pull backend frontend

docker compose up -d --remove-orphans
docker compose ps

docker image prune -f

echo ">> done. running images:"
docker inspect sql-quiz-backend sql-quiz-frontend --format '{{.Name}} -> {{.Config.Image}}'
