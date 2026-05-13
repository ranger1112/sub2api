#!/usr/bin/env bash
# Sync newly-added CLIProxyAPI OpenAI OAuth auth files into sub2api.
#
# Low-load design:
#   1. Read CPA auth JSON files and extract account_id/chatgpt_account_id only.
#   2. Query sub2api Postgres for active OpenAI OAuth account_ids.
#   3. Run the heavier idempotent migration only when at least one new account exists.
#
# Environment overrides:
#   CPA_AUTH_DIR=/root/CLIProxyAPI/auths
#   SUB2API_DIR=/root/sub2api
#   SUB2API_PG_CONTAINER=sub2api-postgres
#   SUB2API_DB=sub2api
#   SUB2API_DB_USER=sub2api
#   SUB2API_SYNC_LOG=/var/log/sub2api/cliproxyapi-account-sync.log
#   SUB2API_SYNC_NO_BACKUP=1          # skip pg_dump backup when new accounts are found
#   SUB2API_SYNC_EXTRA_ARGS="..."      # appended to migrate_cliproxyapi_accounts.py

set -Eeuo pipefail

SUB2API_DIR="${SUB2API_DIR:-/root/sub2api}"
CPA_AUTH_DIR="${CPA_AUTH_DIR:-/root/CLIProxyAPI/auths}"
PG_CONTAINER="${SUB2API_PG_CONTAINER:-sub2api-postgres}"
DB_NAME="${SUB2API_DB:-sub2api}"
DB_USER="${SUB2API_DB_USER:-sub2api}"
LOG_FILE="${SUB2API_SYNC_LOG:-/var/log/sub2api/cliproxyapi-account-sync.log}"
LOCK_FILE="${SUB2API_SYNC_LOCK:-/tmp/sub2api-cliproxyapi-account-sync.lock}"
MIGRATION_SCRIPT="${SUB2API_MIGRATION_SCRIPT:-${SUB2API_DIR}/tools/migrate_cliproxyapi_accounts.py}"

mkdir -p "$(dirname "$LOG_FILE")"

log() {
  printf '[%s] %s\n' "$(date -Is)" "$*" | tee -a "$LOG_FILE"
}

fail() {
  log "ERROR: $*"
  exit 1
}

command -v docker >/dev/null 2>&1 || fail "docker not found"
command -v python3 >/dev/null 2>&1 || fail "python3 not found"
[[ -d "$CPA_AUTH_DIR" ]] || fail "CPA auth dir not found: $CPA_AUTH_DIR"
[[ -f "$MIGRATION_SCRIPT" ]] || fail "migration script not found: $MIGRATION_SCRIPT"

docker inspect "$PG_CONTAINER" >/dev/null 2>&1 || fail "postgres container not found: $PG_CONTAINER"

exec 9>"$LOCK_FILE"
if ! flock -n 9; then
  log "another sync is already running; skip"
  exit 0
fi

source_ids_file="$(mktemp -t cpa-source-ids.XXXXXX)"
db_ids_file="$(mktemp -t sub2api-db-ids.XXXXXX)"
new_ids_file="$(mktemp -t cpa-new-ids.XXXXXX)"
cleanup() {
  rm -f "$source_ids_file" "$db_ids_file" "$new_ids_file"
}
trap cleanup EXIT

python3 - "$CPA_AUTH_DIR" >"$source_ids_file" <<'PY'
import json
import sys
from pathlib import Path

auth_dir = Path(sys.argv[1])
ids = set()
errors = []
for path in sorted(auth_dir.glob('*.json')):
    try:
        raw = json.loads(path.read_text())
    except Exception as exc:
        errors.append(f'{path.name}: {exc}')
        continue
    account_id = str(raw.get('account_id') or raw.get('chatgpt_account_id') or '').strip().lower()
    if account_id:
        ids.add(account_id)

if errors:
    print('\n'.join(f'ERROR {e}' for e in errors), file=sys.stderr)
    sys.exit(2)

for account_id in sorted(ids):
    print(account_id)
PY

source_count="$(wc -l <"$source_ids_file" | tr -d ' ')"
if [[ "$source_count" == "0" ]]; then
  log "no CPA auth account_ids found in $CPA_AUTH_DIR; skip"
  exit 0
fi

docker exec -i "$PG_CONTAINER" psql -U "$DB_USER" -d "$DB_NAME" -At -v ON_ERROR_STOP=1 >"$db_ids_file" <<'SQL'
SELECT lower(credentials->>'chatgpt_account_id')
FROM accounts
WHERE platform = 'openai'
  AND type = 'oauth'
  AND deleted_at IS NULL
  AND COALESCE(credentials->>'chatgpt_account_id', '') <> ''
ORDER BY 1;
SQL

comm -23 <(sort -u "$source_ids_file") <(sort -u "$db_ids_file") >"$new_ids_file"
new_count="$(wc -l <"$new_ids_file" | tr -d ' ')"

if [[ "$new_count" == "0" ]]; then
  log "no new CPA accounts; source=${source_count}; skip migration"
  exit 0
fi

log "found ${new_count} new CPA account(s) out of source=${source_count}; running migration"
log "new account_id sample: $(head -5 "$new_ids_file" | paste -sd, -)"

args=(
  "$MIGRATION_SCRIPT"
  --auth-dir "$CPA_AUTH_DIR"
  --container "$PG_CONTAINER"
  --db "$DB_NAME"
  --db-user "$DB_USER"
  --execute
)

if [[ "${SUB2API_SYNC_NO_BACKUP:-0}" == "1" ]]; then
  args+=(--no-backup)
fi

# Optional extra args are intentionally shell-split for operator-controlled systemd env files.
# shellcheck disable=SC2206
extra_args=( ${SUB2API_SYNC_EXTRA_ARGS:-} )
args+=("${extra_args[@]}")

if python3 "${args[@]}" >>"$LOG_FILE" 2>&1; then
  log "migration finished successfully"
else
  rc=$?
  log "migration failed with exit_code=${rc}; see $LOG_FILE"
  exit "$rc"
fi
