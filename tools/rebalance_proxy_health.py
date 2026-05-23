#!/usr/bin/env python3
"""Check active account proxies and rebalance accounts away from unhealthy exits.

The script is intentionally low-impact:
- only probes proxies that are currently used by active accounts;
- each proxy receives one exit-IP probe and one ChatGPT reachability probe;
- bad proxies are marked error only when --apply is used;
- active accounts on bad proxies are moved to healthy proxies while keeping
  active account count per observed exit IP <= --max-accounts-per-ip;
- accounts that cannot be moved due to insufficient healthy capacity are
  temporarily unscheduled instead of failing the whole run.

It connects to the local Dockerized PostgreSQL via `docker exec sub2api-postgres psql`
so it can run from the host without storing DB credentials.
"""

from __future__ import annotations

import argparse
import concurrent.futures
import datetime as dt
import json
import os
import pathlib
import subprocess
import sys
import time
from collections import Counter
from dataclasses import dataclass, asdict
from typing import Iterable

DEFAULT_DB_CONTAINER = "sub2api-postgres"
DEFAULT_DB_USER = "sub2api"
DEFAULT_DB_NAME = "sub2api"
DEFAULT_BACKUP_ROOT = pathlib.Path("/root/sub2api/deploy/data/proxy-health-auto")


@dataclass(frozen=True)
class ProxyRow:
    id: int
    name: str
    host: str
    port: int
    status: str
    active_accounts: int


@dataclass(frozen=True)
class AccountRow:
    id: int
    name: str
    proxy_id: int


@dataclass
class ProbeResult:
    id: int
    name: str
    host: str
    port: int
    status: str
    active_accounts: int
    exit_ip: str | None
    ip_ok: bool
    chatgpt_ok: bool
    probe_error: str | None
    chatgpt_probe: list[str]
    elapsed_ms: int

    @property
    def healthy(self) -> bool:
        return self.ip_ok and self.chatgpt_ok and bool(self.exit_ip)


class Psql:
    def __init__(self, container: str, user: str, db: str):
        self.container = container
        self.user = user
        self.db = db

    def query_tsv(self, sql: str) -> list[list[str]]:
        cmd = [
            "docker", "exec", self.container,
            "psql", "-U", self.user, "-d", self.db,
            "-At", "-F", "\t", "-c", sql,
        ]
        out = subprocess.check_output(cmd, text=True)
        return [line.split("\t") for line in out.splitlines() if line.strip()]

    def query_text(self, sql: str) -> str:
        cmd = [
            "docker", "exec", self.container,
            "psql", "-U", self.user, "-d", self.db,
            "-At", "-F", "\t", "-c", sql,
        ]
        return subprocess.check_output(cmd, text=True)

    def exec_stdin(self, sql: str) -> str:
        cmd = [
            "docker", "exec", "-i", self.container,
            "psql", "-U", self.user, "-d", self.db, "-v", "ON_ERROR_STOP=1",
        ]
        proc = subprocess.run(cmd, input=sql, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        if proc.returncode != 0:
            raise RuntimeError(proc.stdout)
        return proc.stdout


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--apply", action="store_true", help="apply DB changes; default is dry-run")
    parser.add_argument("--max-accounts-per-ip", type=int, default=15)
    parser.add_argument("--workers", type=int, default=2)
    parser.add_argument("--connect-timeout", type=int, default=2)
    parser.add_argument("--max-time", type=int, default=5)
    parser.add_argument("--temp-unsched-minutes", type=int, default=30)
    parser.add_argument("--max-move-accounts", type=int, default=25, help="safety cap: skip apply when planned account moves exceed this value")
    parser.add_argument("--max-temp-unsched-accounts", type=int, default=20, help="safety cap: skip apply when planned temporary unscheduled accounts exceed this value")
    parser.add_argument("--max-affected-accounts", type=int, default=40, help="safety cap: skip apply when planned move+temporary-unscheduled accounts exceed this value")
    parser.add_argument("--max-new-bad-active-proxies", type=int, default=3, help="safety cap: skip apply when active proxies newly detected as bad exceed this value")
    parser.add_argument("--verbose-moves", action="store_true", help="print per-account move details; default logs summary only")
    parser.add_argument("--backup-root", type=pathlib.Path, default=DEFAULT_BACKUP_ROOT)
    parser.add_argument("--db-container", default=DEFAULT_DB_CONTAINER)
    parser.add_argument("--db-user", default=DEFAULT_DB_USER)
    parser.add_argument("--db-name", default=DEFAULT_DB_NAME)
    parser.add_argument("--chatgpt-url", default="https://chatgpt.com/")
    parser.add_argument("--ip-url", default="https://api.ipify.org")
    return parser.parse_args()


def shell_capture(cmd: list[str], timeout: int) -> str:
    try:
        return subprocess.run(
            cmd,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            timeout=timeout,
            check=False,
        ).stdout.strip()
    except subprocess.TimeoutExpired:
        return "TIMEOUT"


def load_used_proxies(psql: Psql) -> list[ProxyRow]:
    sql = """
    SELECT p.id, p.name, p.host, p.port, p.status, count(a.id) AS active_accounts
    FROM proxies p
    JOIN accounts a ON a.proxy_id = p.id
      AND a.deleted_at IS NULL
      AND a.status = 'active'
    WHERE p.deleted_at IS NULL
    GROUP BY p.id, p.name, p.host, p.port, p.status
    ORDER BY p.port;
    """
    rows = []
    for r in psql.query_tsv(sql):
        rows.append(ProxyRow(id=int(r[0]), name=r[1], host=r[2], port=int(r[3]), status=r[4], active_accounts=int(r[5])))
    return rows


def load_active_accounts(psql: Psql) -> list[AccountRow]:
    sql = """
    SELECT id, name, proxy_id
    FROM accounts
    WHERE deleted_at IS NULL
      AND status = 'active'
      AND proxy_id IS NOT NULL
    ORDER BY proxy_id, id;
    """
    return [AccountRow(id=int(r[0]), name=r[1], proxy_id=int(r[2])) for r in psql.query_tsv(sql)]


def probe_proxy(proxy: ProxyRow, args: argparse.Namespace) -> ProbeResult:
    proxy_url = f"http://{proxy.host}:{proxy.port}"
    started = time.time()
    ip_out = shell_capture([
        "curl", "-sS", "--proxy", proxy_url,
        "--connect-timeout", str(args.connect_timeout),
        "--max-time", str(args.max_time),
        args.ip_url,
    ], timeout=args.max_time + 2)
    ip_ok = bool(ip_out and not ip_out.startswith("curl:") and ip_out != "TIMEOUT" and len(ip_out) < 64)
    exit_ip = ip_out if ip_ok else None

    head = shell_capture([
        "curl", "-sS", "-I", "--proxy", proxy_url,
        "--connect-timeout", str(args.connect_timeout),
        "--max-time", str(args.max_time),
        args.chatgpt_url,
    ], timeout=args.max_time + 2)
    lines = head.splitlines()[:6]
    chatgpt_ok = any(marker in head for marker in ("HTTP/2 103", "HTTP/2 200", "HTTP/1.1 200 OK", "HTTP/2 403", "HTTP/2 429"))
    if any(marker in head for marker in ("SSL connection timeout", "UNEXPECTED_EOF", "Failed to connect", "TIMEOUT")):
        chatgpt_ok = False
    probe_error = None
    if not ip_ok:
        probe_error = f"ip_probe_failed:{ip_out[:120]}"
    if not chatgpt_ok:
        probe_error = (probe_error + ";" if probe_error else "") + f"chatgpt_probe_failed:{head[:160]}"

    return ProbeResult(
        id=proxy.id,
        name=proxy.name,
        host=proxy.host,
        port=proxy.port,
        status=proxy.status,
        active_accounts=proxy.active_accounts,
        exit_ip=exit_ip,
        ip_ok=ip_ok,
        chatgpt_ok=chatgpt_ok,
        probe_error=probe_error,
        chatgpt_probe=lines,
        elapsed_ms=int((time.time() - started) * 1000),
    )


def plan_rebalance(accounts: list[AccountRow], probes: list[ProbeResult], max_per_ip: int) -> tuple[list[dict], list[dict], list[int], dict[str, int]]:
    probe_by_id = {p.id: p for p in probes}
    healthy = [p for p in probes if p.healthy and p.status == "active"]
    bad_ids = sorted(p.id for p in probes if not p.healthy or p.status != "active")

    proxy_count = Counter(a.proxy_id for a in accounts)
    ip_count = Counter()
    for a in accounts:
        p = probe_by_id.get(a.proxy_id)
        if p and p.healthy and p.status == "active" and p.exit_ip:
            ip_count[p.exit_ip] += 1

    move_by_account: dict[int, dict] = {}
    for a in accounts:
        if a.proxy_id in bad_ids:
            p = probe_by_id.get(a.proxy_id)
            move_by_account[a.id] = {
                "id": a.id,
                "name": a.name,
                "old_proxy_id": a.proxy_id,
                "old_port": p.port if p else None,
                "reason": "bad_proxy",
            }

    for ip, count in list(ip_count.items()):
        if count <= max_per_ip:
            continue
        excess = count - max_per_ip
        candidates = [a for a in accounts if probe_by_id.get(a.proxy_id) and probe_by_id[a.proxy_id].exit_ip == ip]
        candidates.sort(key=lambda a: a.id, reverse=True)
        for a in candidates[:excess]:
            p = probe_by_id[a.proxy_id]
            item = move_by_account.setdefault(a.id, {
                "id": a.id,
                "name": a.name,
                "old_proxy_id": a.proxy_id,
                "old_port": p.port,
                "reason": "",
            })
            item["reason"] = ",".join(x for x in [item.get("reason"), f"ip_over_cap:{ip}"] if x)
            ip_count[ip] -= 1

    moving_ids = set(move_by_account)
    mutable_proxy_count = proxy_count.copy()
    mutable_ip_count = Counter()
    for a in accounts:
        if a.id in moving_ids:
            mutable_proxy_count[a.proxy_id] -= 1
            continue
        p = probe_by_id.get(a.proxy_id)
        if p and p.healthy and p.status == "active" and p.exit_ip:
            mutable_ip_count[p.exit_ip] += 1

    healthy_targets = sorted(healthy, key=lambda p: (mutable_proxy_count[p.id], p.port))
    assignments = []
    unschedulable = []
    for item in sorted(move_by_account.values(), key=lambda x: (x["reason"], x["id"])):
        choices = []
        for p in healthy_targets:
            if p.id == item["old_proxy_id"]:
                continue
            if not p.exit_ip or mutable_ip_count[p.exit_ip] >= max_per_ip:
                continue
            choices.append((mutable_ip_count[p.exit_ip], mutable_proxy_count[p.id], p.port, p))
        if not choices:
            item = dict(item)
            item.update({
                "temp_unschedulable_reason": (
                    f"proxy_health_rebalance: no healthy proxy capacity; "
                    f"old_proxy_id={item['old_proxy_id']} old_port={item['old_port']} "
                    f"reason={item['reason']}"
                )[:500],
            })
            unschedulable.append(item)
            continue
        _, _, _, target = min(choices)
        item = dict(item)
        item.update({
            "new_proxy_id": target.id,
            "new_port": target.port,
            "new_exit_ip": target.exit_ip,
        })
        assignments.append(item)
        mutable_proxy_count[target.id] += 1
        mutable_ip_count[target.exit_ip] += 1

    return assignments, unschedulable, bad_ids, dict(mutable_ip_count)


def write_backup(psql: Psql, backup_dir: pathlib.Path, probes: list[ProbeResult], assignments: list[dict], unschedulable: list[dict], bad_ids: list[int], final_ip_counts: dict[str, int]) -> None:
    backup_dir.mkdir(parents=True, exist_ok=True)
    (backup_dir / "proxy_health.json").write_text(json.dumps([asdict(p) for p in probes], ensure_ascii=False, indent=2))
    (backup_dir / "rebalance_plan.json").write_text(json.dumps({
        "assignments": assignments,
        "temp_unschedulable_accounts": unschedulable,
        "bad_proxy_ids": bad_ids,
        "final_ip_counts": final_ip_counts,
    }, ensure_ascii=False, indent=2))

    account_ids = sorted({a["id"] for a in assignments} | {a["id"] for a in unschedulable})
    ids_csv = ",".join(map(str, account_ids)) or "NULL"
    bad_csv = ",".join(map(str, bad_ids)) or "NULL"
    (backup_dir / "accounts_before.tsv").write_text(psql.query_text(
        f"SELECT id,name,proxy_id,status,schedulable,temp_unschedulable_until,temp_unschedulable_reason,updated_at FROM accounts WHERE id IN ({ids_csv}) ORDER BY id;"
    ))
    (backup_dir / "proxies_before.tsv").write_text(psql.query_text(
        f"SELECT id,name,protocol,host,port,status,updated_at FROM proxies WHERE id IN ({bad_csv}) ORDER BY id;"
    ))


def sql_quote(value: str) -> str:
    return "'" + value.replace("'", "''") + "'"


def apply_plan(psql: Psql, assignments: list[dict], unschedulable: list[dict], bad_ids: list[int], temp_unsched_minutes: int) -> str:
    if not assignments and not unschedulable and not bad_ids:
        return "nothing to apply"
    stmts = ["BEGIN;"]
    if assignments:
        values = ",".join(f"({a['id']},{a['new_proxy_id']})" for a in assignments)
        stmts += [
            "CREATE TEMP TABLE proxy_rebalance(account_id bigint primary key, new_proxy_id bigint) ON COMMIT DROP;",
            f"INSERT INTO proxy_rebalance(account_id,new_proxy_id) VALUES {values};",
            """
            UPDATE accounts a
            SET proxy_id = r.new_proxy_id, updated_at = NOW()
            FROM proxy_rebalance r
            WHERE a.id = r.account_id AND a.deleted_at IS NULL;
            """,
        ]
    if unschedulable:
        values = ",".join(
            f"({a['id']},{sql_quote(a['temp_unschedulable_reason'])})"
            for a in unschedulable
        )
        minutes = max(1, int(temp_unsched_minutes))
        stmts += [
            "CREATE TEMP TABLE proxy_rebalance_unsched(account_id bigint primary key, reason text) ON COMMIT DROP;",
            f"INSERT INTO proxy_rebalance_unsched(account_id,reason) VALUES {values};",
            f"""
            UPDATE accounts a
            SET temp_unschedulable_until = NOW() + INTERVAL '{minutes} minutes',
                temp_unschedulable_reason = u.reason,
                updated_at = NOW()
            FROM proxy_rebalance_unsched u
            WHERE a.id = u.account_id AND a.deleted_at IS NULL;
            """,
        ]
    if bad_ids:
        bad_csv = ",".join(map(str, bad_ids))
        stmts.append(f"""
            UPDATE proxies
            SET status = 'error', updated_at = NOW()
            WHERE id IN ({bad_csv}) AND deleted_at IS NULL;
        """)
    stmts.append("COMMIT;")
    return psql.exec_stdin("\n".join(stmts))


def main() -> int:
    args = parse_args()
    psql = Psql(args.db_container, args.db_user, args.db_name)
    proxies = load_used_proxies(psql)
    if not proxies:
        print(json.dumps({"status": "ok", "message": "no active account proxies"}, ensure_ascii=False))
        return 0

    with concurrent.futures.ThreadPoolExecutor(max_workers=max(1, args.workers)) as executor:
        probes = list(executor.map(lambda p: probe_proxy(p, args), proxies))

    accounts = load_active_accounts(psql)
    assignments, unschedulable, bad_ids, final_ip_counts = plan_rebalance(accounts, probes, args.max_accounts_per_ip)
    max_ip_count = max(final_ip_counts.values(), default=0)

    stamp = dt.datetime.now().strftime("%Y%m%dT%H%M%S")
    backup_dir = args.backup_root / stamp
    write_backup(psql, backup_dir, probes, assignments, unschedulable, bad_ids, final_ip_counts)

    active_bad_ids = sorted(p.id for p in probes if p.status == "active" and not p.healthy)
    affected_accounts = len(assignments) + len(unschedulable)
    safety_violations = []
    if len(assignments) > args.max_move_accounts:
        safety_violations.append(f"move_accounts {len(assignments)} > max_move_accounts {args.max_move_accounts}")
    if len(unschedulable) > args.max_temp_unsched_accounts:
        safety_violations.append(
            f"temp_unschedulable_accounts {len(unschedulable)} > max_temp_unsched_accounts {args.max_temp_unsched_accounts}"
        )
    if affected_accounts > args.max_affected_accounts:
        safety_violations.append(f"affected_accounts {affected_accounts} > max_affected_accounts {args.max_affected_accounts}")
    if len(active_bad_ids) > args.max_new_bad_active_proxies:
        safety_violations.append(
            f"new_bad_active_proxies {len(active_bad_ids)} > max_new_bad_active_proxies {args.max_new_bad_active_proxies}"
        )

    summary = {
        "mode": "apply" if args.apply else "dry-run",
        "checked_proxies": len(probes),
        "healthy_proxies": sum(1 for p in probes if p.healthy and p.status == "active"),
        "bad_proxy_ids": bad_ids,
        "new_bad_active_proxy_ids": active_bad_ids,
        "move_accounts": len(assignments),
        "temp_unschedulable_accounts": len(unschedulable),
        "affected_accounts": affected_accounts,
        "temp_unschedulable_minutes": args.temp_unsched_minutes if unschedulable else 0,
        "max_accounts_per_ip_after_plan": max_ip_count,
        "safety_limits": {
            "max_move_accounts": args.max_move_accounts,
            "max_temp_unsched_accounts": args.max_temp_unsched_accounts,
            "max_affected_accounts": args.max_affected_accounts,
            "max_new_bad_active_proxies": args.max_new_bad_active_proxies,
        },
        "safety_violations": safety_violations,
        "backup_dir": str(backup_dir),
    }
    print(json.dumps(summary, ensure_ascii=False, indent=2))
    if args.verbose_moves:
        for a in assignments[:200]:
            print(f"MOVE account={a['id']} proxy={a['old_port']}->{a['new_port']} reason={a['reason']} exit_ip={a['new_exit_ip']}")
        if len(assignments) > 200:
            print(f"... {len(assignments)-200} more moves omitted")
        for a in unschedulable[:200]:
            print(f"TEMP_UNSCHED account={a['id']} proxy={a['old_port']} reason={a['reason']}")
        if len(unschedulable) > 200:
            print(f"... {len(unschedulable)-200} more temp-unsched omitted")

    if args.apply:
        if safety_violations:
            print("SKIP_APPLY_SAFETY_LIMIT: " + "; ".join(safety_violations))
        else:
            print(apply_plan(psql, assignments, unschedulable, bad_ids, args.temp_unsched_minutes))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
