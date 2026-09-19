#!/usr/bin/env bash
set -euo pipefail

BASE="${BASE:-http://127.0.0.1:8081}"
fail=0
SMOKE_TMP="$(mktemp -d "${TMPDIR:-/tmp}/sentence-web-smoke.XXXXXX")"
trap 'rm -rf "$SMOKE_TMP"' EXIT

check() {
  local name="$1"
  shift
  if eval "$@"; then
    echo "PASS $name"
  else
    echo "FAIL $name" >&2
    fail=1
  fi
}

check healthz 'curl --fail --silent --show-error "$BASE/healthz" >/dev/null'
check readyz 'curl --fail --silent --show-error "$BASE/readyz" >/dev/null'
check home 'curl --fail --silent --show-error -o "$SMOKE_TMP/home.html" "$BASE/" && grep -q "id=\"random-content\"" "$SMOKE_TMP/home.html"'
check docs 'code=$(curl --fail --silent --show-error -o "$SMOKE_TMP/docs.html" -w "%{http_code}" "$BASE/docs") && test "$code" = 200'
check submit_page 'curl --fail --silent --show-error -o "$SMOKE_TMP/submit.html" "$BASE/submit" && grep -q "form_token" "$SMOKE_TMP/submit.html"'

check dataset_json 'headers=$(curl --fail --silent --show-error -D - -o "$SMOKE_TMP/sentences.json" "$BASE/dataset/sentences.json") && grep -qi "^etag:" <<<"$headers" && { if command -v jq >/dev/null 2>&1; then jq empty "$SMOKE_TMP/sentences.json"; elif command -v python3 >/dev/null 2>&1; then python3 -m json.tool "$SMOKE_TMP/sentences.json" >/dev/null; else true; fi; }'

check dataset_license 'curl --fail --silent --show-error -o "$SMOKE_TMP/LICENSE.txt" "$BASE/dataset/LICENSE.txt" && grep -q "GNU AFFERO" "$SMOKE_TMP/LICENSE.txt"'

check admin_redirect 'code=$(curl --silent --show-error -o /dev/null -w "%{http_code}" "$BASE/admin/") && test "$code" = "303"'

check unknown_404 'code=$(curl --silent --show-error -o /dev/null -w "%{http_code}" "$BASE/nope") && test "$code" = "404"'

check root_post_405 'code=$(curl --silent --show-error -o /dev/null -w "%{http_code}" -X POST "$BASE/") && test "$code" = "405"'

if [ "$fail" -ne 0 ]; then
  exit 1
fi
echo "ALL PASS"
