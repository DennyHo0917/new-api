#!/bin/sh
set -eu

WINDOW_SECONDS=${WINDOW_SECONDS:-300}
APP_CONTAINER=${APP_CONTAINER:-api-route}
PG_CONTAINER=${PG_CONTAINER:-api-route-postgres}
REDIS_CONTAINER=${REDIS_CONTAINER:-api-route-redis}
STATUS_URL=${STATUS_URL:-http://127.0.0.1:3000/api/status}
DIST_URL=${DIST_URL:-http://127.0.0.1:3000/api/dist/site/info}

metric() {
  printf '%-34s %s\n' "$1" "$2"
}

count_log() {
  pattern=$1
  printf '%s\n' "$app_logs" | grep -Eic "$pattern" || true
}

container_metric() {
  name=$1
  state=$(docker inspect -f '{{.State.Status}}' "$name" 2>/dev/null || printf missing)
  health=$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$name" 2>/dev/null || printf missing)
  restarts=$(docker inspect -f '{{.RestartCount}}' "$name" 2>/dev/null || printf missing)
  metric "container.$name" "state=$state health=$health restarts=$restarts"
}

http_code() {
  curl -sS -o /dev/null -w '%{http_code}' --max-time 10 "$1" 2>/dev/null || printf 000
}

printf 'API Route cutover observation (%ss window)\n' "$WINDOW_SECONDS"
metric api.status "$(http_code "$STATUS_URL")"
metric api.dist_site_info "$(http_code "$DIST_URL")"
container_metric "$APP_CONTAINER"
container_metric "$PG_CONTAINER"
container_metric "$REDIS_CONTAINER"

app_logs=$(docker logs --since "${WINDOW_SECONDS}s" "$APP_CONTAINER" 2>&1 || true)
metric logs.http_5xx "$(count_log '(^|[^0-9])5[0-9][0-9]([^0-9]|$)')"
metric logs.http_429 "$(count_log '(^|[^0-9])429([^0-9]|$)')"
metric logs.timeout "$(count_log 'timeout|deadline exceeded')"
metric logs.subrouter_proxy "$(count_log '\[SubRouter Proxy\]')"
metric logs.migration_success "$(count_log '\[Migration\] Completed')"
metric logs.quota_exhausted "$(count_log 'insufficient_user_quota|quota exhausted')"

db_metrics=$(docker exec "$PG_CONTAINER" sh -c 'psql -X -qAt -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -c "SELECT current_setting('"'"'max_connections'"'"'), count(*) FROM pg_stat_activity; SELECT count(*) FILTER (WHERE type=2), COALESCE(sum(quota) FILTER (WHERE type=2),0), count(*) FILTER (WHERE type=5), count(*) FILTER (WHERE type=1), count(*) FILTER (WHERE type=6) FROM logs WHERE created_at >= extract(epoch from now())::bigint - $1;"' sh "$WINDOW_SECONDS" 2>/dev/null || true)
db_connections=$(printf '%s\n' "$db_metrics" | sed -n '1p')
db_activity=$(printf '%s\n' "$db_metrics" | sed -n '2p')
metric postgres.connections "${db_connections:-unavailable} (max|used)"
metric billing.window "${db_activity:-unavailable} (consume|quota|error|topup|refund)"

redis_metrics=$(docker exec "$REDIS_CONTAINER" sh -c 'redis-cli -a "$REDIS_PASSWORD" --no-auth-warning INFO clients memory 2>/dev/null | grep -E "^(connected_clients|used_memory_human):"' 2>/dev/null | tr -d '\r' | tr '\n' ' ' || true)
metric redis.runtime "${redis_metrics:-unavailable}"
