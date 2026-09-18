#!/bin/sh
set -eu

container=${MARIADB_TEST_CONTAINER:-MariaDB}
go_cmd=${GO_CMD:-go}
cache_base=${XDG_CACHE_HOME:-${HOME}/.cache}
tmp_dir=${GOTMPDIR:-$cache_base/sa-tmp}
mkdir -p "$tmp_dir"

db_pass=$(docker exec "$container" sh -c 'printf %s "${MARIADB_ROOT_PASSWORD:-$MYSQL_ROOT_PASSWORD}"')
if [ -z "$db_pass" ]; then
  echo "MariaDB container has no supported root password variable" >&2
  exit 1
fi

if [ "$#" -eq 0 ]; then
  set -- -v ./internal/integration
fi

MYSQL_TEST_DSN="root:${db_pass}@tcp(127.0.0.1:3306)/mysql" GOTMPDIR="$tmp_dir" \
  "$go_cmd" test "$@"
