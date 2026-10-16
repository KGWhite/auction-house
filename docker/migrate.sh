#!/bin/sh
# Wrapper around the goose CLI: `migrate` applies all migrations, `migrate down`
# rolls back one, `migrate status` shows the state. Needs DATABASE_URL.
set -eu
[ $# -eq 0 ] && set -- up
exec goose -dir /migrations postgres "$DATABASE_URL" "$@"
