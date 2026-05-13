#!/usr/bin/env python3
"""Migrate CLIProxyAPI OAuth auth files into sub2api accounts.

This tool mirrors the manual migration flow used for this deployment:

1. Read CLIProxyAPI `auths/*.json` files.
2. Validate source identity uniqueness by email and account_id.
3. Backup the sub2api PostgreSQL database before mutation.
4. Upsert OpenAI OAuth accounts into sub2api by `chatgpt_account_id`/email.
5. Keep account management display name as the email address.
6. Keep token expiry only in `credentials.expires_at` by default; do not set
   account-level `accounts.expires_at`, because that field auto-pauses accounts.
7. Soft-delete duplicate active OpenAI OAuth rows for imported identities.
8. Audit source coverage, display names, token fields, and uniqueness.

The script intentionally avoids printing token values.
"""

from __future__ import annotations

import argparse
import base64
import datetime as dt
import hashlib
import json
import os
import re
import subprocess
import sys
import tempfile
from pathlib import Path
from typing import Any

DEFAULT_AUTH_DIR = "/root/CLIProxyAPI/auths"
DEFAULT_BACKUP_DIR = "/root/backups/sub2api/db"
DEFAULT_CONTAINER = "sub2api-postgres"
DEFAULT_DB = "sub2api"
DEFAULT_DB_USER = "sub2api"
OPENAI_CODEX_CLIENT_ID = "app_EMoamEEZ73f0CkXaXp7hrann"


class MigrationError(RuntimeError):
    pass


def log(message: str) -> None:
    print(message, flush=True)


def run(cmd: list[str], *, input_text: str | None = None, capture: bool = False) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        cmd,
        input=input_text,
        text=True,
        stdout=subprocess.PIPE if capture else None,
        stderr=subprocess.PIPE if capture else None,
        check=True,
    )


def parse_time(value: str) -> dt.datetime | None:
    value = value.strip()
    if not value:
        return None
    try:
        return dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError:
        return None


def decode_jwt_payload(token: str) -> dict[str, Any] | None:
    """Decode a JWT payload without validating the signature.

    The migration only uses this to preserve non-authoritative display metadata
    already embedded in OpenAI-issued tokens, such as chatgpt_plan_type. It is
    not used for authorization decisions.
    """
    token = token.strip()
    if token.count(".") != 2:
        return None
    payload = token.split(".", 2)[1]
    payload += "=" * ((4 - len(payload) % 4) % 4)
    try:
        decoded = base64.urlsafe_b64decode(payload.encode())
        value = json.loads(decoded)
    except Exception:
        return None
    return value if isinstance(value, dict) else None


def extract_openai_plan_type(*tokens: str, raw: dict[str, Any]) -> str:
    for key in ("plan_type", "planType", "chatgpt_plan_type", "chatgptPlanType"):
        value = raw.get(key)
        if isinstance(value, str) and value.strip():
            return value.strip().lower()

    for token in tokens:
        payload = decode_jwt_payload(token)
        if not payload:
            continue
        auth = payload.get("https://api.openai.com/auth")
        if not isinstance(auth, dict):
            continue
        value = auth.get("chatgpt_plan_type")
        if isinstance(value, str) and value.strip():
            return value.strip().lower()
    return ""


def read_auth_files(auth_dir: Path, *, client_id: str) -> list[dict[str, Any]]:
    if not auth_dir.is_dir():
        raise MigrationError(f"auth dir does not exist: {auth_dir}")

    now = dt.datetime.now(dt.timezone.utc)
    imported_at = now.isoformat()
    items: list[dict[str, Any]] = []

    for path in sorted(auth_dir.glob("*.json")):
        try:
            raw = json.loads(path.read_text())
        except Exception as exc:  # noqa: BLE001 - include file path in CLI error
            raise MigrationError(f"failed to parse {path}: {exc}") from exc

        email = str(raw.get("email") or "").strip()
        account_id = str(raw.get("account_id") or raw.get("chatgpt_account_id") or "").strip()
        access_token = str(raw.get("access_token") or "").strip()
        refresh_token = str(raw.get("refresh_token") or "").strip()
        id_token = str(raw.get("id_token") or "").strip()
        expired = str(raw.get("expired") or raw.get("expires_at") or "").strip()
        last_refresh = str(raw.get("last_refresh") or raw.get("lastRefreshedAt") or "").strip()
        disabled = bool(raw.get("disabled"))
        plan_type = extract_openai_plan_type(id_token, access_token, raw=raw)

        missing = []
        if not email:
            missing.append("email")
        if not account_id:
            missing.append("account_id")
        if not access_token:
            missing.append("access_token")
        if not refresh_token:
            missing.append("refresh_token")
        if not id_token:
            missing.append("id_token")
        if missing:
            raise MigrationError(f"{path.name}: missing required fields: {', '.join(missing)}")

        priority_raw = raw.get("priority")
        try:
            priority = int(priority_raw) if priority_raw not in (None, "") else 50
        except (TypeError, ValueError):
            priority = 50

        expires_in: int | None = None
        expiry_dt = parse_time(expired)
        if expiry_dt is not None:
            expires_in = max(0, int((expiry_dt.astimezone(dt.timezone.utc) - now).total_seconds()))

        credentials: dict[str, Any] = {
            "access_token": access_token,
            "refresh_token": refresh_token,
            "id_token": id_token,
            "client_id": client_id,
            "email": email,
            "chatgpt_account_id": account_id,
        }
        if expired:
            credentials["expires_at"] = expired
        if expires_in is not None:
            credentials["expires_in"] = expires_in
        if plan_type:
            credentials["plan_type"] = plan_type

        extra: dict[str, Any] = {
            "email": email,
            "openai_passthrough": True,
            "import_source": "cliproxyapi_auths",
            "imported_at": imported_at,
            "cliproxy_source_file": path.name,
            "cliproxy_type": raw.get("type") or "codex",
        }
        if plan_type:
            extra["openai_plan_type"] = plan_type
        if last_refresh:
            extra["cliproxy_last_refresh"] = last_refresh
        for source_key, dest_key in (
            ("_path", "cliproxy_path"),
            ("prefix", "cliproxy_prefix"),
            ("proxy_url", "cliproxy_proxy_url"),
        ):
            if raw.get(source_key):
                extra[dest_key] = raw[source_key]

        items.append(
            {
                "name": email,
                "platform": "openai",
                "type": "oauth",
                "credentials": credentials,
                "extra": extra,
                "priority": priority,
                "status": "disabled" if disabled else "active",
                "schedulable": not disabled,
                "source_file": path.name,
            }
        )

    if not items:
        raise MigrationError(f"no auth JSON files found in {auth_dir}")

    validate_source_uniqueness(items)
    return items


def validate_source_uniqueness(items: list[dict[str, Any]]) -> None:
    by_email: dict[str, str] = {}
    by_account: dict[str, str] = {}
    for item in items:
        email = item["name"].lower()
        account_id = item["credentials"]["chatgpt_account_id"].lower()
        source = item["source_file"]
        if email in by_email:
            raise MigrationError(f"duplicate source email {email}: {by_email[email]} and {source}")
        if account_id in by_account:
            raise MigrationError(f"duplicate source account_id {account_id}: {by_account[account_id]} and {source}")
        by_email[email] = source
        by_account[account_id] = source


def write_payload(items: list[dict[str, Any]]) -> Path:
    fd, name = tempfile.mkstemp(prefix="cliproxy-auths-", suffix=".json")
    with os.fdopen(fd, "w") as fh:
        json.dump(items, fh, ensure_ascii=False)
    return Path(name)


def sha256_file(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as fh:
        for chunk in iter(lambda: fh.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def backup_database(args: argparse.Namespace) -> Path:
    backup_dir = Path(args.backup_dir)
    backup_dir.mkdir(parents=True, exist_ok=True)
    ts = dt.datetime.now().strftime("%Y%m%dT%H%M%S")
    backup = backup_dir / f"sub2api-before-cliproxy-migration-{ts}.dump"
    log(f"[backup] writing {backup}")
    with backup.open("wb") as fh:
        subprocess.run(
            ["docker", "exec", args.container, "pg_dump", "-U", args.db_user, "-d", args.db, "-Fc"],
            stdout=fh,
            check=True,
        )
    log(f"[backup] sha256={sha256_file(backup)} size={backup.stat().st_size} bytes")
    return backup


def psql(args: argparse.Namespace, sql: str, *, capture: bool = False) -> str:
    cmd = ["docker", "exec", "-i", args.container, "psql", "-U", args.db_user, "-d", args.db, "-v", "ON_ERROR_STOP=1"]
    if capture:
        cmd.extend(["-At"])
    proc = run(cmd, input_text=sql, capture=capture)
    return proc.stdout if capture else ""


def copy_payload_to_container(args: argparse.Namespace, payload_path: Path) -> str:
    remote = f"/tmp/{payload_path.name}"
    run(["docker", "cp", str(payload_path), f"{args.container}:{remote}"])
    run(["docker", "exec", args.container, "chmod", "0644", remote])
    return remote


def migration_sql(remote_payload: str, *, keep_account_expires: bool, soft_delete_duplicates: bool) -> str:
    def account_expires_expr(alias: str) -> str:
        if not keep_account_expires:
            return "NULL"
        if alias == "payload":
            return "NULLIF(payload->'credentials'->>'expires_at', '')::timestamptz"
        if alias == "i":
            return "NULLIF(i.credential_expires_at, '')::timestamptz"
        return f"NULLIF({alias}.payload->'credentials'->>'expires_at', '')::timestamptz"

    account_expires_from_c = account_expires_expr("c")
    account_expires_from_payload = account_expires_expr("payload")
    account_expires_from_i = account_expires_expr("i")
    auto_pause_update = "FALSE" if not keep_account_expires else "TRUE"
    duplicate_delete_predicate = "TRUE" if soft_delete_duplicates else "FALSE"
    return f"""
BEGIN;
CREATE TEMP TABLE import_accounts(payload jsonb) ON COMMIT DROP;
INSERT INTO import_accounts(payload)
SELECT * FROM jsonb_array_elements(pg_read_file('{remote_payload}')::jsonb);

CREATE TEMP TABLE import_flat AS
SELECT
    payload,
    lower(payload->'credentials'->>'chatgpt_account_id') AS account_key,
    lower(payload->>'name') AS email_key,
    payload->>'name' AS email_name,
    payload->'credentials'->>'expires_at' AS credential_expires_at,
    lower(payload->'credentials'->>'plan_type') AS plan_type
FROM import_accounts;

-- Pick one canonical active row for each imported identity. Prefer account_id matches, then email matches, then lower id.
CREATE TEMP TABLE canonical_existing AS
WITH candidates AS (
    SELECT
        i.account_key,
        i.email_key,
        a.id,
        CASE WHEN lower(a.credentials->>'chatgpt_account_id') = i.account_key THEN 0 ELSE 1 END AS match_rank
    FROM import_flat i
    JOIN accounts a ON a.platform = 'openai'
        AND a.type = 'oauth'
        AND a.deleted_at IS NULL
        AND (
            lower(a.credentials->>'chatgpt_account_id') = i.account_key
            OR lower(COALESCE(a.credentials->>'email', a.extra->>'email', a.name)) = i.email_key
            OR lower(a.name) = i.email_key
        )
), ranked AS (
    SELECT *, row_number() OVER (PARTITION BY account_key ORDER BY match_rank, id) AS rn
    FROM candidates
)
SELECT account_key, email_key, id
FROM ranked
WHERE rn = 1;

-- Soft-delete non-canonical duplicates among active OpenAI OAuth rows for imported account_ids/emails.
WITH duplicate_rows AS (
    SELECT DISTINCT a.id
    FROM import_flat i
    JOIN accounts a ON a.platform = 'openai'
        AND a.type = 'oauth'
        AND a.deleted_at IS NULL
        AND (
            lower(a.credentials->>'chatgpt_account_id') = i.account_key
            OR lower(COALESCE(a.credentials->>'email', a.extra->>'email', a.name)) = i.email_key
            OR lower(a.name) = i.email_key
        )
    LEFT JOIN canonical_existing c ON c.account_key = i.account_key AND c.id = a.id
    WHERE {duplicate_delete_predicate} AND c.id IS NULL
)
UPDATE accounts a
SET deleted_at = now(),
    updated_at = now(),
    schedulable = false,
    notes = concat_ws(E'\n', a.notes, 'Soft-deleted by CLIProxyAPI migration: duplicate imported identity; canonical active row retained.')
FROM duplicate_rows d
WHERE a.id = d.id;

-- Update canonical rows from CLIProxyAPI data.
WITH canonical_payload AS (
    SELECT c.id, i.payload
    FROM canonical_existing c
    JOIN import_flat i ON i.account_key = c.account_key
)
UPDATE accounts a
SET name = c.payload->>'name',
    credentials = COALESCE(a.credentials, '{{}}'::jsonb) || (c.payload->'credentials'),
    extra = COALESCE(a.extra, '{{}}'::jsonb) || (c.payload->'extra'),
    priority = COALESCE((c.payload->>'priority')::int, a.priority),
    status = c.payload->>'status',
    schedulable = (c.payload->>'schedulable')::boolean,
    expires_at = {account_expires_from_c},
    auto_pause_on_expired = {auto_pause_update},
    updated_at = now()
FROM canonical_payload c
WHERE a.id = c.id;

-- Insert rows missing after canonical matching.
WITH missing AS (
    SELECT i.payload
    FROM import_flat i
    WHERE NOT EXISTS (
        SELECT 1
        FROM accounts a
        WHERE a.platform = 'openai'
          AND a.type = 'oauth'
          AND a.deleted_at IS NULL
          AND lower(a.credentials->>'chatgpt_account_id') = i.account_key
    )
)
INSERT INTO accounts (
    name, platform, type, credentials, extra, concurrency, priority, status, schedulable,
    expires_at, auto_pause_on_expired, created_at, updated_at, rate_multiplier
)
SELECT payload->>'name',
       'openai',
       'oauth',
       payload->'credentials',
       payload->'extra',
       3,
       COALESCE((payload->>'priority')::int, 50),
       payload->>'status',
       (payload->>'schedulable')::boolean,
       {account_expires_from_payload},
       {auto_pause_update},
       now(),
       now(),
       1.0
FROM missing;

-- Enforce email display and JSON email fields for all imported active rows.
WITH imported AS (
    SELECT account_key, email_name
    FROM import_flat
)
UPDATE accounts a
SET name = i.email_name,
    credentials = jsonb_set(COALESCE(a.credentials, '{{}}'::jsonb), '{{email}}', to_jsonb(i.email_name), true),
    extra = jsonb_set(COALESCE(a.extra, '{{}}'::jsonb), '{{email}}', to_jsonb(i.email_name), true),
    expires_at = {account_expires_from_i},
    auto_pause_on_expired = {auto_pause_update},
    updated_at = now()
FROM imported i
WHERE a.platform = 'openai'
  AND a.type = 'oauth'
  AND a.deleted_at IS NULL
  AND lower(a.credentials->>'chatgpt_account_id') = i.account_key;

-- Bind imported accounts to plan-specific groups:
--   plus -> all active OpenAI subscription groups + codex-plus
--   free -> codex-free only
-- The migration owns these plan groups for imported identities and replaces
-- any stale codex-plus/codex-free/subscription membership for them.
CREATE TEMP TABLE imported_active_accounts AS
SELECT a.id AS account_id, i.plan_type
FROM import_flat i
JOIN accounts a ON a.platform = 'openai'
    AND a.type = 'oauth'
    AND a.deleted_at IS NULL
    AND lower(a.credentials->>'chatgpt_account_id') = i.account_key;

CREATE TEMP TABLE migration_managed_groups AS
SELECT id
FROM groups
WHERE deleted_at IS NULL
  AND platform = 'openai'
  AND (
    name IN ('codex-plus', 'codex-free')
    OR subscription_type = 'subscription'
  );

CREATE TEMP TABLE desired_account_groups AS
SELECT ia.account_id, g.id AS group_id
FROM imported_active_accounts ia
JOIN groups g ON g.deleted_at IS NULL
    AND g.platform = 'openai'
    AND ia.plan_type = 'plus'
    AND (g.name = 'codex-plus' OR g.subscription_type = 'subscription')
UNION
SELECT ia.account_id, g.id AS group_id
FROM imported_active_accounts ia
JOIN groups g ON g.deleted_at IS NULL
    AND g.platform = 'openai'
    AND ia.plan_type = 'free'
    AND g.name = 'codex-free';

DELETE FROM account_groups ag
USING imported_active_accounts ia, migration_managed_groups mg
WHERE ag.account_id = ia.account_id
  AND ag.group_id = mg.id
  AND NOT EXISTS (
    SELECT 1
    FROM desired_account_groups dag
    WHERE dag.account_id = ag.account_id
      AND dag.group_id = ag.group_id
  );

INSERT INTO account_groups (account_id, group_id, priority, created_at)
SELECT dag.account_id,
       dag.group_id,
       row_number() OVER (PARTITION BY dag.account_id ORDER BY g.subscription_type = 'subscription', g.name, dag.group_id)::int,
       now()
FROM desired_account_groups dag
JOIN groups g ON g.id = dag.group_id
ON CONFLICT (account_id, group_id) DO NOTHING;

COMMIT;
"""


def preview_sql(remote_payload: str) -> str:
    return f"""
BEGIN;
CREATE TEMP TABLE import_accounts(payload jsonb) ON COMMIT DROP;
INSERT INTO import_accounts(payload)
SELECT * FROM jsonb_array_elements(pg_read_file('{remote_payload}')::jsonb);

WITH import_flat AS (
    SELECT payload,
           lower(payload->'credentials'->>'chatgpt_account_id') AS account_key,
           lower(payload->>'name') AS email_key
    FROM import_accounts
), matches AS (
    SELECT i.account_key, count(a.id) AS active_matches
    FROM import_flat i
    LEFT JOIN accounts a ON a.platform = 'openai'
        AND a.type = 'oauth'
        AND a.deleted_at IS NULL
        AND (
            lower(a.credentials->>'chatgpt_account_id') = i.account_key
            OR lower(COALESCE(a.credentials->>'email', a.extra->>'email', a.name)) = i.email_key
            OR lower(a.name) = i.email_key
        )
    GROUP BY i.account_key
)
SELECT
    (SELECT count(*) FROM import_flat) AS source_count,
    (SELECT count(*) FROM matches WHERE active_matches > 0) AS matched_sources,
    (SELECT count(*) FROM matches WHERE active_matches = 0) AS would_create,
    (SELECT COALESCE(sum(GREATEST(active_matches - 1, 0)), 0) FROM matches) AS duplicate_rows_that_would_soft_delete;
COMMIT;
"""


def audit(args: argparse.Namespace, items: list[dict[str, Any]], *, quiet: bool = False) -> dict[str, Any]:
    source_ids = {item["credentials"]["chatgpt_account_id"].lower(): item for item in items}
    source_emails = {item["name"].lower(): item for item in items}
    sql = """
SELECT COALESCE(json_agg(row_to_json(t)), '[]'::json) FROM (
    SELECT id,
           name,
           status,
           schedulable,
           deleted_at,
           credentials->>'email' AS cred_email,
           extra->>'email' AS extra_email,
           credentials->>'chatgpt_account_id' AS account_id,
           credentials ? 'access_token' AS has_access,
           credentials ? 'refresh_token' AS has_refresh,
           credentials ? 'id_token' AS has_id,
           credentials ? 'client_id' AS has_client_id,
           credentials ? 'expires_at' AS has_credential_expires_at,
           credentials->>'plan_type' AS plan_type,
           COALESCE((
               SELECT json_agg(g.name ORDER BY g.name)
               FROM account_groups ag
               JOIN groups g ON g.id = ag.group_id
               WHERE ag.account_id = a.id
                 AND g.deleted_at IS NULL
           ), '[]'::json) AS group_names,
           expires_at AS account_expires_at,
           auto_pause_on_expired
    FROM accounts a
    WHERE platform = 'openai' AND type = 'oauth' AND deleted_at IS NULL
    ORDER BY id
) t;
"""
    raw = psql(args, sql, capture=True).strip()
    rows = json.loads(raw or "[]")

    by_account: dict[str, list[dict[str, Any]]] = {}
    by_email: dict[str, list[dict[str, Any]]] = {}
    for row in rows:
        account_id = str(row.get("account_id") or "").lower()
        name = str(row.get("name") or "").lower()
        email = str(row.get("cred_email") or row.get("extra_email") or row.get("name") or "").lower()
        if account_id in source_ids:
            by_account.setdefault(account_id, []).append(row)
        if name in source_emails or email in source_emails:
            by_email.setdefault(name or email, []).append(row)

    missing = []
    bad_display = []
    bad_email_json = []
    bad_token_fields = []
    bad_plan_type = []
    bad_plan_groups = []
    bad_account_expires = []

    group_sql = """
SELECT COALESCE(json_agg(name ORDER BY name), '[]'::json)
FROM groups
WHERE deleted_at IS NULL
  AND platform = 'openai'
  AND subscription_type = 'subscription';
"""
    subscription_group_names = json.loads(psql(args, group_sql, capture=True).strip() or "[]")

    for account_key, item in source_ids.items():
        rows_for_account = by_account.get(account_key, [])
        if not rows_for_account:
            missing.append(item["name"])
            continue
        row = rows_for_account[0]
        email = item["name"]
        if row.get("name") != email:
            bad_display.append([email, row.get("id"), row.get("name")])
        if row.get("cred_email") != email or row.get("extra_email") != email:
            bad_email_json.append([email, row.get("id"), row.get("cred_email"), row.get("extra_email")])
        if not all(row.get(k) for k in ("has_access", "has_refresh", "has_id", "has_client_id", "has_credential_expires_at")):
            bad_token_fields.append([email, row.get("id")])
        expected_plan = str(item["credentials"].get("plan_type") or "")
        if expected_plan and row.get("plan_type") != expected_plan:
            bad_plan_type.append([email, row.get("id"), expected_plan, row.get("plan_type")])
        if expected_plan in ("plus", "free"):
            actual_groups = sorted(str(name) for name in (row.get("group_names") or []))
            if expected_plan == "plus":
                expected_groups = sorted(["codex-plus", *subscription_group_names])
            else:
                expected_groups = ["codex-free"]
            if actual_groups != expected_groups:
                bad_plan_groups.append([email, row.get("id"), expected_plan, expected_groups, actual_groups])
        if not args.keep_account_expires and row.get("account_expires_at") is not None:
            bad_account_expires.append([email, row.get("id"), row.get("account_expires_at")])

    duplicate_account_ids = {key: [r["id"] for r in value] for key, value in by_account.items() if len(value) > 1}
    duplicate_emails = {key: [r["id"] for r in value] for key, value in by_email.items() if len(value) > 1}

    report = {
        "source_count": len(items),
        "matched_source_by_account_id": len(items) - len(missing),
        "missing_count": len(missing),
        "duplicate_imported_account_id_count": len(duplicate_account_ids),
        "duplicate_imported_email_display_count": len(duplicate_emails),
        "bad_display_count": len(bad_display),
        "bad_email_json_count": len(bad_email_json),
        "bad_token_field_count": len(bad_token_fields),
        "bad_plan_type_count": len(bad_plan_type),
        "bad_plan_group_count": len(bad_plan_groups),
        "bad_account_expires_count": len(bad_account_expires),
        "missing": missing,
        "duplicate_account_ids": duplicate_account_ids,
        "duplicate_emails": duplicate_emails,
        "bad_display": bad_display,
        "bad_email_json": bad_email_json,
        "bad_token_fields": bad_token_fields,
        "bad_plan_type": bad_plan_type,
        "bad_plan_groups": bad_plan_groups,
        "bad_account_expires": bad_account_expires,
    }
    if not quiet:
        log(json.dumps(report, ensure_ascii=False, indent=2))
    return report


def report_has_failures(report: dict[str, Any]) -> bool:
    failure_keys = (
        "missing_count",
        "duplicate_imported_account_id_count",
        "duplicate_imported_email_display_count",
        "bad_display_count",
        "bad_email_json_count",
        "bad_token_field_count",
        "bad_plan_type_count",
        "bad_plan_group_count",
        "bad_account_expires_count",
    )
    return any(int(report.get(key, 0)) != 0 for key in failure_keys)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Migrate CLIProxyAPI auth JSON accounts into sub2api PostgreSQL accounts.",
        formatter_class=argparse.ArgumentDefaultsHelpFormatter,
    )
    parser.add_argument("--auth-dir", default=DEFAULT_AUTH_DIR, help="CLIProxyAPI auth JSON directory")
    parser.add_argument("--container", default=DEFAULT_CONTAINER, help="Postgres Docker container name")
    parser.add_argument("--db", default=DEFAULT_DB, help="Postgres database name")
    parser.add_argument("--db-user", default=DEFAULT_DB_USER, help="Postgres user")
    parser.add_argument("--backup-dir", default=DEFAULT_BACKUP_DIR, help="Host directory for pg_dump backups")
    parser.add_argument("--client-id", default=OPENAI_CODEX_CLIENT_ID, help="OpenAI/Codex OAuth client_id to store with refresh_token")
    parser.add_argument("--execute", action="store_true", help="Apply migration. Without this flag, only preview/audit is run")
    parser.add_argument("--no-backup", action="store_true", help="Skip pg_dump backup when --execute is used")
    parser.add_argument("--keep-account-expires", action="store_true", help="Also set account-level accounts.expires_at from token expiry. Not recommended")
    parser.add_argument("--no-soft-delete-duplicates", action="store_true", help="Do not soft-delete duplicate active OpenAI OAuth rows")
    parser.add_argument("--audit-only", action="store_true", help="Only run current-state audit against source files")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    auth_dir = Path(args.auth_dir)
    try:
        items = read_auth_files(auth_dir, client_id=args.client_id)
        log(f"[source] loaded {len(items)} auth files from {auth_dir}")

        if args.audit_only:
            report = audit(args, items)
            return 2 if report_has_failures(report) else 0

        payload = write_payload(items)
        try:
            remote_payload = copy_payload_to_container(args, payload)
            if not args.execute:
                log("[dry-run] no database changes will be made. Use --execute to migrate.")
                log("[dry-run] preview:")
                print(psql(args, preview_sql(remote_payload), capture=True).strip())
                log("[dry-run] current audit:")
                audit(args, items)
                return 0

            if not args.no_backup:
                backup_database(args)
            else:
                log("[backup] skipped by --no-backup")

            log("[migrate] applying idempotent migration")
            psql(
                args,
                migration_sql(
                    remote_payload,
                    keep_account_expires=args.keep_account_expires,
                    soft_delete_duplicates=not args.no_soft_delete_duplicates,
                ),
            )
            log("[audit] verifying migrated accounts")
            report = audit(args, items)
            if report_has_failures(report):
                raise MigrationError("post-migration audit failed")
            log("[done] migration audit passed")
            return 0
        finally:
            if "remote_payload" in locals():
                try:
                    run(["docker", "exec", args.container, "rm", "-f", remote_payload])
                except Exception:
                    pass
            try:
                payload.unlink(missing_ok=True)
            except OSError:
                pass
    except subprocess.CalledProcessError as exc:
        if exc.stdout:
            sys.stderr.write(exc.stdout)
        if exc.stderr:
            sys.stderr.write(exc.stderr)
        sys.stderr.write(f"command failed: {' '.join(exc.cmd)}\n")
        return exc.returncode or 1
    except MigrationError as exc:
        sys.stderr.write(f"error: {exc}\n")
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
