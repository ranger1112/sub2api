#!/usr/bin/env python3
"""
Fetch a Clash/Mihomo subscription URL and update all slot configs + main config.
After updating, reloads each slot via the mihomo external-controller API and
resets any error-state proxies in the sub2api database that are now healthy.

Usage:
    python3 update_mihomo_subscription.py --url <SUB_URL>
    MIHOMO_SUB_URL=<url> python3 update_mihomo_subscription.py

Environment variables:
    MIHOMO_SUB_URL      Subscription URL (required if --url not given)
    MIHOMO_CONFIG_DIR   Mihomo config root (default: /root/.config/mihomo)
    MIHOMO_SLOT_COUNT   Number of slots (default: 36)
"""

import argparse
import json
import os
import pathlib
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.request
import concurrent.futures
from datetime import datetime

import yaml  # pip install pyyaml

# ---------------------------------------------------------------------------
# Config
# ---------------------------------------------------------------------------
DEFAULT_CONFIG_DIR = pathlib.Path("/root/.config/mihomo")
DEFAULT_SLOT_COUNT = 36
# Nodes that live outside the subscription and must be preserved as-is
LEGACY_NODE_NAMES = {"HK-01", "JP-01"}
# Mihomo external-controller base port for slot-0
SLOT_CTRL_BASE_PORT = 9000
# sub2api postgres connection (via docker exec)
POSTGRES_CONTAINER = "sub2api-postgres"
POSTGRES_USER = "sub2api"
POSTGRES_DB = "sub2api"


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def log(msg: str) -> None:
    ts = datetime.now().strftime("%Y-%m-%dT%H:%M:%S")
    print(f"[{ts}] {msg}", flush=True)


def fetch_subscription(url: str) -> dict:
    log(f"拉取订阅: {url}")
    req = urllib.request.Request(url, headers={"User-Agent": "ClashMeta/1.0"})
    with urllib.request.urlopen(req, timeout=30) as resp:
        raw = resp.read().decode("utf-8")
    data = yaml.safe_load(raw)
    proxies = data.get("proxies", [])
    log(f"订阅节点数: {len(proxies)}")
    return data


def backup_configs(config_dir: pathlib.Path) -> pathlib.Path:
    ts = datetime.now().strftime("%Y%m%dT%H%M%S")
    backup = config_dir / f"slots.backup.sub-update.{ts}"
    shutil.copytree(config_dir / "slots", backup)
    shutil.copy2(config_dir / "config.yaml", config_dir / f"config.yaml.bak.{ts}")
    log(f"备份完成: {backup}")
    return backup


def update_main_config(config_dir: pathlib.Path, sub_proxies: list) -> None:
    main_path = config_dir / "config.yaml"
    with open(main_path) as f:
        main = yaml.safe_load(f)

    # Preserve legacy nodes
    legacy = {p["name"]: p for p in main.get("proxies", []) if p["name"] in LEGACY_NODE_NAMES}
    sub_by_name = {p["name"]: p for p in sub_proxies}
    new_proxies = list(legacy.values()) + sub_proxies
    main["proxies"] = new_proxies

    # Rebuild proxy-group proxy lists
    all_names = [p["name"] for p in new_proxies]
    for g in main.get("proxy-groups", []):
        if "proxies" in g:
            g["proxies"] = all_names

    with open(main_path, "w") as f:
        yaml.dump(main, f, allow_unicode=True, default_flow_style=False, sort_keys=False)
    log(f"主配置已更新: {len(new_proxies)} 个节点 (含 {len(legacy)} 个旧节点)")


def update_slot_configs(config_dir: pathlib.Path, slot_count: int, sub_by_name: dict) -> dict:
    updated, skipped = 0, 0
    results = {}
    for i in range(slot_count):
        path = config_dir / "slots" / f"slot-{i}.yaml"
        if not path.exists():
            continue
        with open(path) as f:
            slot = yaml.safe_load(f)
        proxies = slot.get("proxies", [])
        if not proxies:
            continue
        name = proxies[0]["name"]
        if name in sub_by_name:
            slot["proxies"] = [sub_by_name[name]]
            for g in slot.get("proxy-groups", []):
                if "proxies" in g:
                    g["proxies"] = [name]
            with open(path, "w") as f:
                yaml.dump(slot, f, allow_unicode=True, default_flow_style=False, sort_keys=False)
            updated += 1
            results[i] = name
        else:
            skipped += 1
    log(f"slot 配置更新: {updated} 个已更新, {skipped} 个保持不变 (旧节点)")
    return results


def reload_slots(slot_count: int, config_dir: pathlib.Path) -> tuple[int, int]:
    """Reload each slot via mihomo external-controller API."""
    ok = fail = 0

    def reload_one(i):
        port = SLOT_CTRL_BASE_PORT + i
        config_path = str(config_dir / "slots" / f"slot-{i}.yaml")
        body = json.dumps({"path": config_path}).encode()
        req = urllib.request.Request(
            f"http://127.0.0.1:{port}/configs?force=true",
            data=body,
            method="PUT",
            headers={"Content-Type": "application/json"},
        )
        try:
            with urllib.request.urlopen(req, timeout=5):
                pass
            return True
        except Exception:
            return False

    with concurrent.futures.ThreadPoolExecutor(max_workers=16) as ex:
        futures = {ex.submit(reload_one, i): i for i in range(slot_count)}
        for fut in concurrent.futures.as_completed(futures):
            if fut.result():
                ok += 1
            else:
                fail += 1

    log(f"mihomo reload: {ok} 成功, {fail} 失败")
    return ok, fail


def probe_proxy(port: int, connect_timeout: int, max_time: int) -> tuple[int, bool, bool]:
    proxy_url = f"http://172.21.0.1:{port}"
    r = subprocess.run(
        ["curl", "-sS", "--proxy", proxy_url,
         "--connect-timeout", str(connect_timeout), "--max-time", str(max_time),
         "https://api.ipify.org"],
        capture_output=True, text=True,
    )
    ip_ok = bool(r.stdout and not r.stdout.startswith("curl:") and len(r.stdout) < 64)

    r2 = subprocess.run(
        ["curl", "-sS", "-I", "--proxy", proxy_url,
         "--connect-timeout", str(connect_timeout), "--max-time", str(max_time),
         "https://chatgpt.com/"],
        capture_output=True, text=True,
    )
    head = r2.stdout[:200]
    chatgpt_ok = any(m in head for m in ("HTTP/2 103", "HTTP/2 200", "HTTP/1.1 200", "HTTP/2 403", "HTTP/2 429"))
    if any(m in head for m in ("SSL connection timeout", "UNEXPECTED_EOF", "Failed to connect", "TIMEOUT")):
        chatgpt_ok = False

    return port, ip_ok, chatgpt_ok


def reset_healthy_error_proxies(connect_timeout: int, max_time: int, workers: int) -> int:
    """Find error-state proxies in sub2api DB, probe them, reset healthy ones to active."""
    psql_cmd = ["docker", "exec", POSTGRES_CONTAINER,
                "psql", "-U", POSTGRES_USER, "-d", POSTGRES_DB, "-t", "-A", "-F", "\t"]

    try:
        out = subprocess.check_output(
            psql_cmd + ["-c", "SELECT id, port FROM proxies WHERE status = 'error' AND deleted_at IS NULL;"],
            text=True,
        )
    except Exception as e:
        log(f"数据库查询失败，跳过代理状态重置: {e}")
        return 0

    rows = [line.split("\t") for line in out.strip().splitlines() if line.strip()]
    if not rows:
        log("没有 error 状态的代理，跳过重置")
        return 0

    log(f"检测 {len(rows)} 个 error 代理...")
    port_to_id = {int(r[1]): int(r[0]) for r in rows}

    with concurrent.futures.ThreadPoolExecutor(max_workers=workers) as ex:
        results = list(ex.map(lambda p: probe_proxy(p, connect_timeout, max_time), port_to_id.keys()))

    healthy_ids = [port_to_id[port] for port, ip_ok, chatgpt_ok in results if ip_ok and chatgpt_ok]
    if not healthy_ids:
        log("没有可恢复的代理")
        return 0

    ids_csv = ",".join(str(i) for i in healthy_ids)
    subprocess.run(
        psql_cmd + ["-c", f"UPDATE proxies SET status='active', updated_at=NOW() WHERE id IN ({ids_csv});"],
        check=True, capture_output=True,
    )
    log(f"已将 {len(healthy_ids)} 个代理恢复为 active: ids={healthy_ids}")
    return len(healthy_ids)


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main() -> None:
    parser = argparse.ArgumentParser(description="Update mihomo subscription and reload slots")
    parser.add_argument("--url", default=os.environ.get("MIHOMO_SUB_URL", ""),
                        help="Subscription URL (or set MIHOMO_SUB_URL)")
    parser.add_argument("--config-dir", default=os.environ.get("MIHOMO_CONFIG_DIR", str(DEFAULT_CONFIG_DIR)),
                        type=pathlib.Path)
    parser.add_argument("--slots", type=int, default=int(os.environ.get("MIHOMO_SLOT_COUNT", DEFAULT_SLOT_COUNT)))
    parser.add_argument("--connect-timeout", type=int, default=5)
    parser.add_argument("--max-time", type=int, default=12)
    parser.add_argument("--workers", type=int, default=10)
    parser.add_argument("--skip-health-reset", action="store_true",
                        help="Skip resetting error proxies in sub2api DB")
    args = parser.parse_args()

    if not args.url:
        print("ERROR: 需要提供订阅 URL (--url 或 MIHOMO_SUB_URL 环境变量)", file=sys.stderr)
        sys.exit(1)

    log("=== 开始更新 mihomo 订阅 ===")

    # 1. Fetch subscription
    sub_data = fetch_subscription(args.url)
    sub_proxies = sub_data.get("proxies", [])
    sub_by_name = {p["name"]: p for p in sub_proxies}

    # 2. Backup
    backup_configs(args.config_dir)

    # 3. Update configs
    update_main_config(args.config_dir, sub_proxies)
    update_slot_configs(args.config_dir, args.slots, sub_by_name)

    # 4. Reload slots via API
    time.sleep(0.5)
    reload_slots(args.slots, args.config_dir)

    # 5. Reset healthy error proxies in sub2api DB
    if not args.skip_health_reset:
        time.sleep(2)  # give mihomo a moment to apply new configs
        reset_healthy_error_proxies(args.connect_timeout, args.max_time, args.workers)

    log("=== 订阅更新完成 ===")


if __name__ == "__main__":
    main()
