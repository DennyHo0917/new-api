#!/bin/sh

set -u

DEPLOY_DIR=${DEPLOY_DIR:-/data/api-route}
COMPOSE_FILE=${COMPOSE_FILE:-$DEPLOY_DIR/docker-compose.yml}
BACKUP_DIR=${BACKUP_DIR:-$DEPLOY_DIR/backups}
BACKUP_MAX_AGE_HOURS=${BACKUP_MAX_AGE_HOURS:-24}
MAX_DISK_USE_PERCENT=${MAX_DISK_USE_PERCENT:-85}
APP_CONTAINER=${APP_CONTAINER:-api-route}
POSTGRES_CONTAINER=${POSTGRES_CONTAINER:-api-route-postgres}
REDIS_CONTAINER=${REDIS_CONTAINER:-api-route-redis}
LOCAL_STATUS_URL=${LOCAL_STATUS_URL:-http://127.0.0.1:3000/api/status}
LOCAL_DIST_URL=${LOCAL_DIST_URL:-http://127.0.0.1:3000/api/dist/site/info}
REQUIRED_ENV_NAMES=${REQUIRED_ENV_NAMES:-SQL_DSN REDIS_CONN_STRING SESSION_SECRET}

PASS_COUNT=0
FAIL_COUNT=0
WARN_COUNT=0

pass() {
  PASS_COUNT=$((PASS_COUNT + 1))
  printf 'PASS  %s\n' "$1"
}

fail() {
  FAIL_COUNT=$((FAIL_COUNT + 1))
  printf 'FAIL  %s\n' "$1"
}

warn() {
  WARN_COUNT=$((WARN_COUNT + 1))
  printf 'WARN  %s\n' "$1"
}

check_container() {
  name=$1
  state=$(docker inspect -f '{{.State.Status}}' "$name" 2>/dev/null) || {
    fail "container $name is missing"
    return
  }
  health=$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$name" 2>/dev/null)
  if [ "$state" = running ] && [ "$health" = healthy ]; then
    pass "container $name is running and healthy"
  else
    fail "container $name state=$state health=$health"
  fi
}

if command -v docker >/dev/null 2>&1; then
  pass 'docker is available'
else
  fail 'docker is unavailable'
  printf '\nRESULT FAIL (%s passed, %s failed, %s warnings)\n' "$PASS_COUNT" "$FAIL_COUNT" "$WARN_COUNT"
  exit 1
fi

if [ -f "$COMPOSE_FILE" ] && docker compose -f "$COMPOSE_FILE" config -q >/dev/null 2>&1; then
  pass 'docker compose configuration is valid'
else
  fail "docker compose configuration is invalid or missing: $COMPOSE_FILE"
fi

check_container "$APP_CONTAINER"
check_container "$POSTGRES_CONTAINER"
check_container "$REDIS_CONTAINER"

missing_env=$(docker exec "$APP_CONTAINER" sh -c '
  for name do
    printenv "$name" >/dev/null 2>&1 || printf "%s " "$name"
  done
' sh $REQUIRED_ENV_NAMES 2>/dev/null)
if [ -z "$missing_env" ]; then
  pass 'required application environment variables are present (values hidden)'
else
  fail "missing application environment variables: $missing_env"
fi

if docker exec "$APP_CONTAINER" sh -c '[ "$SESSION_SECRET" != random_string ] && [ "${#SESSION_SECRET}" -ge 32 ]' >/dev/null 2>&1; then
  pass 'session secret is non-default and at least 32 characters'
else
  fail 'session secret is missing, default, or shorter than 32 characters'
fi

if docker exec "$APP_CONTAINER" sh -c '[ "$SESSION_COOKIE_SECURE" = true ] && [ -n "$SESSION_COOKIE_TRUSTED_URL" ]' >/dev/null 2>&1; then
  pass 'secure session cookie and trusted browser origins are configured'
else
  fail 'SESSION_COOKIE_SECURE=true and SESSION_COOKIE_TRUSTED_URL are required for cutover'
fi

if command -v curl >/dev/null 2>&1 && curl -fsS --max-time 10 "$LOCAL_STATUS_URL" >/dev/null; then
  pass "backend status endpoint responds: $LOCAL_STATUS_URL"
else
  fail "backend status endpoint failed: $LOCAL_STATUS_URL"
fi

if command -v curl >/dev/null 2>&1 && curl -fsS --max-time 15 "$LOCAL_DIST_URL" >/dev/null; then
  pass "SubRouter distribution endpoint responds: $LOCAL_DIST_URL"
else
  fail "SubRouter distribution endpoint failed: $LOCAL_DIST_URL"
fi

pg_version=$(docker exec "$POSTGRES_CONTAINER" sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atc "show server_version"' 2>/dev/null)
case "$pg_version" in
  15.*) pass "PostgreSQL version is $pg_version" ;;
  '') fail 'PostgreSQL version query failed' ;;
  *) fail "PostgreSQL major version is not 15: $pg_version" ;;
esac

user_count=$(docker exec "$POSTGRES_CONTAINER" sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atc "select count(*) from users"' 2>/dev/null)
case "$user_count" in
  ''|*[!0-9]*) fail 'production user count query failed' ;;
  *) pass "production user count is $user_count" ;;
esac

disk_use=$(df -P "$DEPLOY_DIR" 2>/dev/null | awk 'NR == 2 { gsub("%", "", $5); print $5 }')
case "$disk_use" in
  ''|*[!0-9]*) fail "disk usage query failed: $DEPLOY_DIR" ;;
  *)
    if [ "$disk_use" -le "$MAX_DISK_USE_PERCENT" ]; then
      pass "disk usage is ${disk_use}% (limit ${MAX_DISK_USE_PERCENT}%)"
    else
      fail "disk usage is ${disk_use}% (limit ${MAX_DISK_USE_PERCENT}%)"
    fi
    ;;
esac

backup_file=${BACKUP_FILE:-}
if [ -z "$backup_file" ] && [ -d "$BACKUP_DIR" ]; then
  backup_file=$(find "$BACKUP_DIR" -maxdepth 1 -type f \( -name '*.dump' -o -name '*.sql' -o -name '*.sql.gz' \) -printf '%T@ %p\n' 2>/dev/null | sort -nr | sed -n '1s/^[^ ]* //p')
fi
if [ -n "$backup_file" ] && [ -f "$backup_file" ]; then
  backup_age_hours=$((($(date +%s) - $(stat -c %Y "$backup_file")) / 3600))
  if [ "$backup_age_hours" -le "$BACKUP_MAX_AGE_HOURS" ]; then
    pass "database backup is ${backup_age_hours}h old: $backup_file"
  else
    fail "database backup is ${backup_age_hours}h old (limit ${BACKUP_MAX_AGE_HOURS}h): $backup_file"
  fi
else
  fail "no database backup found in $BACKUP_DIR (or set BACKUP_FILE)"
fi

image_id=$(docker inspect -f '{{.Image}}' "$APP_CONTAINER" 2>/dev/null)
if [ -n "$image_id" ]; then
  pass "backend image id is $image_id"
else
  fail 'backend image id is unavailable'
fi

if command -v nginx >/dev/null 2>&1; then
  if nginx -t >/dev/null 2>&1; then
    pass 'nginx configuration is valid'
  else
    fail 'nginx configuration test failed'
  fi
else
  warn 'nginx command is unavailable; skipped configuration test'
fi

printf '\nRESULT '
if [ "$FAIL_COUNT" -eq 0 ]; then
  printf 'PASS'
else
  printf 'FAIL'
fi
printf ' (%s passed, %s failed, %s warnings)\n' "$PASS_COUNT" "$FAIL_COUNT" "$WARN_COUNT"

[ "$FAIL_COUNT" -eq 0 ]
