#!/bin/sh
set -eu
base_url=${BASE_URL:-http://127.0.0.1:8080}
curl --fail --silent --show-error "$base_url/healthz" >/dev/null
curl --fail --silent --show-error "$base_url/readyz" >/dev/null
curl --fail --silent --show-error "$base_url/version" >/dev/null
curl --fail --silent --show-error "$base_url/api/v1/categories" >/dev/null
curl --fail --silent --show-error "$base_url/api/v1/sentences/random" >/dev/null
