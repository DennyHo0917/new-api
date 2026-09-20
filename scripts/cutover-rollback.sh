#!/bin/sh
set -eu

DEPLOY_DIR=${DEPLOY_DIR:-/data/api-route}
APP_CONTAINER=${APP_CONTAINER:-api-route}
APP_SERVICE=${APP_SERVICE:-new-api}
CURRENT_IMAGE=${CURRENT_IMAGE:-api-route-backend:latest}
ROLLBACK_IMAGE=${ROLLBACK_IMAGE:-api-route-backend:rollback}

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  exit 1
}

health() {
  docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$APP_CONTAINER" 2>/dev/null || true
}

case "${1:-}" in
  prepare)
    [ "$(health)" = healthy ] || fail "$APP_CONTAINER is not healthy"
    image_id=$(docker inspect -f '{{.Image}}' "$APP_CONTAINER")
    docker image tag "$image_id" "$ROLLBACK_IMAGE"
    printf 'PASS: rollback image prepared (%s)\n' "$image_id"
    ;;
  status)
    printf 'container_health=%s\n' "$(health)"
    printf 'current_image=%s\n' "$(docker image inspect -f '{{.Id}}' "$CURRENT_IMAGE" 2>/dev/null || printf missing)"
    printf 'rollback_image=%s\n' "$(docker image inspect -f '{{.Id}}' "$ROLLBACK_IMAGE" 2>/dev/null || printf missing)"
    ;;
  backend)
    rollback_id=$(docker image inspect -f '{{.Id}}' "$ROLLBACK_IMAGE" 2>/dev/null) || fail "rollback image is missing; run prepare before deployment"
    docker image tag "$rollback_id" "$CURRENT_IMAGE"
    cd "$DEPLOY_DIR"
    docker compose up -d --no-deps --force-recreate "$APP_SERVICE"
    attempts=0
    while [ "$attempts" -lt 30 ]; do
      [ "$(health)" = healthy ] && {
        printf 'PASS: backend rolled back to %s\n' "$rollback_id"
        exit 0
      }
      attempts=$((attempts + 1))
      sleep 2
    done
    fail "rollback container did not become healthy"
    ;;
  *)
    printf 'usage: %s prepare|status|backend\n' "$0" >&2
    exit 2
    ;;
esac
