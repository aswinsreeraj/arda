#!/usr/bin/env bash
set -euo pipefail

# Resolve repository root relative to script location
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"

# Source local .env if available
if [[ -f "${REPO_ROOT}/.env" ]]; then
  set -a
  # shellcheck disable=SC1091
  source "${REPO_ROOT}/.env"
  set +a
fi

# Database connection parameters
DB_HOST="${POSTGRES_HOST:-127.0.0.1}"
DB_PORT="${POSTGRES_PORT:-5432}"
DB_NAME="${POSTGRES_DB:-arda}"
DB_USER="${POSTGRES_USER:-arda_user}"
DB_PASSWORD="${POSTGRES_PASSWORD:-arda_password}"
DATABASE_URL="${DATABASE_URL:-postgres://${DB_USER}:${DB_PASSWORD}@${DB_HOST}:${DB_PORT}/${DB_NAME}?sslmode=disable}"
MIGRATIONS_DIR="${REPO_ROOT}/migrations"

usage() {
  cat <<EOF
Usage: $(basename "$0") <command> [arguments]

Reproducible migration runner for ARDA using golang-migrate.

Commands:
  up              Apply all pending migrations
  down [N]        Rollback N migrations (default: 1)
  version         Print current migration version
  status          Alias for version
  reset           Drop all database objects and re-apply all migrations from scratch
  force <version> Force migration version (recovers from dirty state)

Requirements:
  Either 'migrate' CLI in PATH or Docker must be available.
EOF
}

run_migrate() {
  if command -v migrate >/dev/null 2>&1; then
    migrate -path "${MIGRATIONS_DIR}" -database "${DATABASE_URL}" "$@"
  elif command -v docker >/dev/null 2>&1; then
    docker run --rm --network host \
      -v "${MIGRATIONS_DIR}:/migrations:ro" \
      migrate/migrate:v4.18.2 \
      -path=/migrations \
      -database "${DATABASE_URL}" "$@"
  else
    echo "Error: Neither 'migrate' CLI nor 'docker' is available to run migrations." >&2
    exit 1
  fi
}

COMMAND="${1:-}"
shift || true

case "${COMMAND}" in
  up)
    echo "Applying all pending migrations..."
    run_migrate up "$@"
    echo "Migrations applied successfully."
    ;;
  down)
    STEPS="${1:-1}"
    echo "Rolling back ${STEPS} migration(s)..."
    run_migrate down "${STEPS}"
    echo "Rollback complete."
    ;;
  version|status)
    run_migrate version
    ;;
  force)
    if [[ $# -lt 1 ]]; then
      echo "Error: 'force' requires a version argument (e.g. $(basename "$0") force 1)" >&2
      exit 1
    fi
    echo "Forcing migration version to $1..."
    run_migrate force "$1"
    ;;
  reset)
    echo "WARNING: Resetting local database! All data will be removed."
    echo "Dropping all database objects..."
    run_migrate drop -f
    echo "Re-applying all migrations..."
    run_migrate up
    echo "Database reset complete."
    ;;
  help|-h|--help)
    usage
    exit 0
    ;;
  *)
    echo "Error: Unknown command '${COMMAND}'" >&2
    usage >&2
    exit 1
    ;;
esac
