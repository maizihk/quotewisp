#!/usr/bin/env bash
# End-to-end smoke: admin create → submit → login → approve → read API poll.
# Requires MYSQL_TEST_DSN or a local MariaDB/MySQL container (see resolve_admin_dsn).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN="${BIN:-$ROOT/bin/sentence-api}"
APP_PORT="${APP_PORT:-18082}"
APP_BASE="http://127.0.0.1:${APP_PORT}"
API_BASE="$APP_BASE"
WEB_BASE="$APP_BASE"
WEB_SECRET_KEY="${WEB_SECRET_KEY:-01234567890123456789012345678901}"
SITE_CONTACT="${SITE_CONTACT:-e2e@example.com}"
ADMIN_USER="${ADMIN_USER:-e2eadmin}"
ADMIN_PASS="${ADMIN_PASS:-e2e-test-pass-123}"
POLL_INTERVAL="${SNAPSHOT_POLL_INTERVAL:-2s}"
POLL_TIMEOUT="${E2E_POLL_TIMEOUT:-45}"
FIXTURE="${FIXTURE:-$ROOT/testdata/sentences.json}"

APP_PID=""
DB_CREATED=0
DB_NAME=""
COOKIE_JAR=""
TMPDIR_E2E=""

log() { printf '%s\n' "$*" >&2; }
die() { log "FAIL: $*"; exit 1; }

cleanup() {
  local code=$?
  if [ -n "$APP_PID" ]; then kill "$APP_PID" 2>/dev/null || true; wait "$APP_PID" 2>/dev/null || true; fi
  if [ "$DB_CREATED" -eq 1 ] && [ -n "$DB_NAME" ]; then
    mysql_exec "DROP DATABASE IF EXISTS \`$DB_NAME\`" 2>/dev/null || true
  fi
  if [ -n "$TMPDIR_E2E" ] && [ -d "$TMPDIR_E2E" ]; then rm -rf "$TMPDIR_E2E"; fi
  if [ "$code" -ne 0 ]; then log "e2e-web aborted (exit $code)"; fi
}
trap cleanup EXIT

extract_hidden() {
  local html="$1" name="$2"
  printf '%s' "$html" | sed -n "s/.*name=\"${name}\" value=\"\\([^\"]*\\)\".*/\\1/p" | head -1
}

extract_first_submission_id() {
  local html="$1"
  printf '%s' "$html" | sed -n 's|.*href="/admin/submissions/\([0-9][0-9]*\)".*|\1|p' | head -1
}

extract_sentence_uuid() {
  local html="$1"
  printf '%s' "$html" | sed -n 's|.*href="/admin/sentences/\([0-9a-f-]\{36\}\)".*|\1|p' | head -1
}

json_field() {
  local json="$1" field="$2"
  if command -v jq >/dev/null 2>&1; then
    jq -r ".data.${field} // empty" <<<"$json"
    return
  fi
  printf '%s' "$json" | sed -n "s/.*\"${field}\":\"\\([^\"]*\\)\".*/\\1/p" | head -1
}

resolve_admin_dsn() {
  if [ -n "${MYSQL_TEST_DSN:-}" ]; then
    ADMIN_DSN="$MYSQL_TEST_DSN"
    return 0
  fi
  local container="${MARIADB_TEST_CONTAINER:-MariaDB}"
  if ! docker ps --format '{{.Names}}' 2>/dev/null | grep -qx "$container"; then
    die "set MYSQL_TEST_DSN or start MariaDB container ($container)"
  fi
  MARIADB_CONTAINER="$container"
  MARIADB_ROOT_PASSWORD="$(docker exec "$container" sh -c 'printf %s "${MARIADB_ROOT_PASSWORD:-$MYSQL_ROOT_PASSWORD}"')"
  if [ -z "$MARIADB_ROOT_PASSWORD" ]; then
    die "MariaDB container has no MARIADB_ROOT_PASSWORD or MYSQL_ROOT_PASSWORD"
  fi
  ADMIN_DSN="root:${MARIADB_ROOT_PASSWORD}@tcp(127.0.0.1:3306)/mysql"
}

parse_dsn() {
  # user:pass@tcp(host:port)/db — password may contain URL-encoded bytes.
  if ! command -v python3 >/dev/null 2>&1; then
    die "python3 required to parse MYSQL_TEST_DSN when not using docker exec mysql"
  fi
  eval "$(ADMIN_DSN="$ADMIN_DSN" python3 - <<'PY'
import os, re, sys
from urllib.parse import unquote
dsn = os.environ["ADMIN_DSN"]
m = re.match(r"^([^:@/]+):(.+)@tcp\(([^:]+):(\d+)\)/([^?]*)", dsn)
if not m:
    sys.exit("invalid DSN format")
user, pw, host, port, db = m.group(1), unquote(m.group(2)), m.group(3), m.group(4), m.group(5)
for k, v in [("MYSQL_USER", user), ("MYSQL_PASS", pw), ("MYSQL_HOST", host), ("MYSQL_PORT", port), ("MYSQL_ADMIN_DB", db)]:
    print(f"{k}={v!r}")
PY
)"
}

mysql_exec() {
  local sql="$1"
  if [ -n "${MARIADB_CONTAINER:-}" ]; then
    docker exec -i "$MARIADB_CONTAINER" mariadb -uroot -p"${MARIADB_ROOT_PASSWORD}" -e "$sql"
    return
  fi
  parse_dsn
  if command -v mysql >/dev/null 2>&1; then
    MYSQL_PWD="$MYSQL_PASS" mysql -h"$MYSQL_HOST" -P"$MYSQL_PORT" -u"$MYSQL_USER" -e "$sql"
    return
  fi
  die "need mysql client or MariaDB container for database setup"
}

dsn_for_db() {
  local db="$1"
  if [ -n "${MARIADB_CONTAINER:-}" ]; then
    printf 'root:%s@tcp(127.0.0.1:3306)/%s?parseTime=true&multiStatements=true&charset=utf8mb4' \
      "$MARIADB_ROOT_PASSWORD" "$db"
    return
  fi
  parse_dsn
  printf '%s:%s@tcp(%s:%s)/%s?parseTime=true&multiStatements=true&charset=utf8mb4' \
    "$MYSQL_USER" "$MYSQL_PASS" "$MYSQL_HOST" "$MYSQL_PORT" "$db"
}

wait_http() {
  local url="$1" label="$2" tries="${3:-60}"
  local i=1
  while [ "$i" -le "$tries" ]; do
    if curl --fail --silent --show-error "$url" >/dev/null 2>&1; then
      log "ready: $label"
      return 0
    fi
    sleep 1
    i=$((i + 1))
  done
  die "$label not ready at $url"
}

ensure_binary() {
  if [ -x "$BIN" ]; then return; fi
  log "building $BIN"
  (cd "$ROOT" && CGO_ENABLED=0 go build -trimpath -o "$BIN" ./cmd/api)
}

main() {
  resolve_admin_dsn
  ensure_binary
  [ -f "$FIXTURE" ] || die "fixture not found: $FIXTURE"

  DB_NAME="sentence_api_e2e_$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')"
  log "creating isolated database $DB_NAME"
  mysql_exec "CREATE DATABASE \`$DB_NAME\` CHARACTER SET utf8mb4 COLLATE utf8mb4_bin"
  DB_CREATED=1

  MYSQL_DSN="$(dsn_for_db "$DB_NAME")"
  export MYSQL_DSN

  log "migrate up"
  "$BIN" migrate up

  log "import fixture $FIXTURE"
  "$BIN" import --file "$FIXTURE" >/dev/null

  log "create admin $ADMIN_USER"
  printf '%s\n' "$ADMIN_PASS" | "$BIN" web admin create --username "$ADMIN_USER" --password-stdin >/dev/null

  TMPDIR_E2E="$(mktemp -d)"
  mkdir -p "$TMPDIR_E2E/data"
  chmod 700 "$TMPDIR_E2E/data"
  if [ "$(id -u)" -eq 0 ]; then chown 65532:65532 "$TMPDIR_E2E/data"; fi
  COOKIE_JAR="$TMPDIR_E2E/cookies.txt"
  APP_LOG="$TMPDIR_E2E/app.log"

  log "start combined API and web on $APP_BASE (SNAPSHOT_POLL_INTERVAL=$POLL_INTERVAL)"
  WEB_SECRET_KEY="$WEB_SECRET_KEY" SITE_CONTACT="$SITE_CONTACT" COOKIE_SECURE=false \
    HTTP_ADDR="127.0.0.1:${APP_PORT}" DATA_DIR="$TMPDIR_E2E/data" SNAPSHOT_POLL_INTERVAL="$POLL_INTERVAL" \
    "$BIN" >>"$APP_LOG" 2>&1 &
  APP_PID=$!

  wait_http "$APP_BASE/readyz" "combined app"

  E2E_CONTENT="e2e-smoke-$(date +%s)-$RANDOM"
  log "GET /submit"
  submit_page="$(curl --fail --silent --show-error "$WEB_BASE/submit")"
  form_token="$(extract_hidden "$submit_page" form_token)"
  [ -n "$form_token" ] || die "missing form_token on /submit"

  log "wait 6s for form token min age"
  sleep 6

  log "POST /submit"
  code="$(curl --silent --show-error -o "$TMPDIR_E2E/submit.out" -w '%{http_code}' \
    -X POST "$WEB_BASE/submit" \
    --data-urlencode "form_token=$form_token" \
    --data-urlencode "content=$E2E_CONTENT" \
    --data-urlencode "category=original" \
    --data-urlencode "source=e2e-source" \
    --data-urlencode "author=" \
    --data-urlencode "contact=e2e@example.com" \
    --data-urlencode "agree=on")"
  if [ "$code" != "303" ]; then
    log "submit response ($code): $(cat "$TMPDIR_E2E/submit.out")"
    die "submit expected 303, got $code"
  fi
  log "PASS submit"

  log "GET /admin/login"
  login_page="$(curl --fail --silent --show-error -c "$COOKIE_JAR" -b "$COOKIE_JAR" "$WEB_BASE/admin/login")"
  login_token="$(extract_hidden "$login_page" form_token)"
  [ -n "$login_token" ] || die "missing login form_token"

  log "POST /admin/login"
  code="$(curl --silent --show-error -o "$TMPDIR_E2E/login.out" -w '%{http_code}' \
    -c "$COOKIE_JAR" -b "$COOKIE_JAR" \
    -X POST "$WEB_BASE/admin/login" \
    --data-urlencode "form_token=$login_token" \
    --data-urlencode "username=$ADMIN_USER" \
    --data-urlencode "password=$ADMIN_PASS")"
  if [ "$code" != "303" ]; then
    log "login response ($code): $(cat "$TMPDIR_E2E/login.out")"
    die "login expected 303, got $code"
  fi
  log "PASS login"

  log "GET /admin/ overview"
  overview_page="$(curl --fail --silent --show-error -c "$COOKIE_JAR" -b "$COOKIE_JAR" "$WEB_BASE/admin/")"
  printf '%s' "$overview_page" | grep -q '概况' || die "admin home missing 概况"

  log "GET /admin/submissions pending list"
  pending_page="$(curl --fail --silent --show-error -c "$COOKIE_JAR" -b "$COOKIE_JAR" "$WEB_BASE/admin/submissions")"
  sub_id="$(extract_first_submission_id "$pending_page")"
  [ -n "$sub_id" ] || die "no pending submission id in admin list"

  log "GET /admin/submissions/$sub_id"
  detail_page="$(curl --fail --silent --show-error -c "$COOKIE_JAR" -b "$COOKIE_JAR" "$WEB_BASE/admin/submissions/$sub_id")"
  csrf="$(extract_hidden "$detail_page" csrf_token)"
  [ -n "$csrf" ] || die "missing csrf_token on submission detail"

  log "POST /admin/submissions/$sub_id/approve"
  code="$(curl --silent --show-error -o "$TMPDIR_E2E/approve.out" -w '%{http_code}' \
    -c "$COOKIE_JAR" -b "$COOKIE_JAR" \
    -X POST "$WEB_BASE/admin/submissions/$sub_id/approve" \
    --data-urlencode "csrf_token=$csrf" \
    --data-urlencode "content=$E2E_CONTENT" \
    --data-urlencode "category=original" \
    --data-urlencode "source=e2e-source" \
    --data-urlencode "author=")"
  if [ "$code" != "303" ]; then
    log "approve response ($code): $(cat "$TMPDIR_E2E/approve.out")"
    die "approve expected 303, got $code"
  fi
  log "PASS approve"

  log "GET /admin/submissions/$sub_id for sentence UUID"
  approved_page="$(curl --fail --silent --show-error -c "$COOKIE_JAR" -b "$COOKIE_JAR" "$WEB_BASE/admin/submissions/$sub_id")"
  sentence_uuid="$(extract_sentence_uuid "$approved_page")"
  [ -n "$sentence_uuid" ] || die "missing sentence UUID after approve"

  log "poll read API for sentence $sentence_uuid (timeout ${POLL_TIMEOUT}s)"
  deadline=$(( $(date +%s) + POLL_TIMEOUT ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    resp="$(curl --silent --show-error -w $'\n%{http_code}' "$API_BASE/api/v1/sentences/$sentence_uuid" || true)"
    body="${resp%$'\n'*}"
    status="${resp##*$'\n'}"
    if [ "$status" = "200" ]; then
      got="$(json_field "$body" content)"
      if [ "$got" = "$E2E_CONTENT" ]; then
        log "PASS read API content match"
        log "ALL PASS"
        exit 0
      fi
      die "read API 200 but content mismatch: got=$got want=$E2E_CONTENT"
    fi
    sleep 1
  done
  die "read API did not return 200 with expected content within ${POLL_TIMEOUT}s (last status=$status)"
}

main "$@"
