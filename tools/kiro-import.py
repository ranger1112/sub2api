#!/usr/bin/env python3
"""
一键把本地 Kiro IDE 登录后的凭据导入 / 更新到 sub2api 服务。

用法:
  1) 先登录一次(二选一,token 只有登录后才会写到本地缓存):
       - 在 Kiro IDE 里登录,或
       - 用命令行:kiro-cli login --use-device-flow(无需 IDE,浏览器点一下授权即可)
  2) 跑一条命令:  python tools/kiro-import.py
     (Windows 也可双击 tools/kiro-import.bat)

它会:
  - 读 ~/.aws/sso/cache/ 下的 kiro-auth-token.json(IDE)或 device-sso-lsp-token.json(CLI),
    二者都在则取最近修改的;IdC 的 clientId/clientSecret 若不在 token 文件里,会自动找注册文件
  - 自动判断 social / idc / external_idp(外部 IdP,如 Microsoft Entra),拼出 sub2api 需要的凭据字段
  - 登录 sub2api 管理端,按名字「有则更新、无则创建」(重跑即刷新 token,不产生重复账号)
  - 全程不打印任何密钥明文

可用环境变量覆盖默认(适配你的部署):
  SUB2API_URL            默认 http://127.0.0.1:8090
  SUB2API_ADMIN_EMAIL    默认 admin@example.com
  SUB2API_ADMIN_PASSWORD 默认 admin123
  KIRO_ACCOUNT_NAME      默认 kiro-local(账号在 sub2api 里的名字,用于 upsert 匹配)
  KIRO_GROUP_ID          默认 1(绑定的分组;Kiro 挂在 anthropic 分组下)
  KIRO_CACHE_DIR         默认 ~/.aws/sso/cache
"""
import json
import os
import sys
import urllib.request
import urllib.error

BASE = os.environ.get("SUB2API_URL", "http://127.0.0.1:8090").rstrip("/")
ADMIN_EMAIL = os.environ.get("SUB2API_ADMIN_EMAIL", "admin@example.com")
ADMIN_PASSWORD = os.environ.get("SUB2API_ADMIN_PASSWORD", "admin123")
ACCOUNT_NAME = os.environ.get("KIRO_ACCOUNT_NAME", "kiro-local")
GROUP_ID = int(os.environ.get("KIRO_GROUP_ID", "1"))
CACHE_DIR = os.environ.get("KIRO_CACHE_DIR", os.path.join(os.path.expanduser("~"), ".aws", "sso", "cache"))


def die(msg):
    print("✗ " + msg)
    sys.exit(1)


def api(method, path, token=None, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(BASE + path, data=data, method=method)
    req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    try:
        with urllib.request.urlopen(req, timeout=20) as r:
            return r.status, json.loads(r.read().decode() or "{}")
    except urllib.error.HTTPError as e:
        raw = e.read().decode()
        try:
            return e.code, json.loads(raw)
        except Exception:
            return e.code, {"_raw": raw[:300]}
    except Exception as e:
        die("连接 sub2api 失败(%s):%s。确认服务在跑:%s" % (path, e, BASE))


def _first(d, *keys):
    """返回 d 中第一个非空字符串字段(容错 camelCase / snake_case 命名差异)。"""
    if not isinstance(d, dict):
        return ""
    for k in keys:
        v = d.get(k)
        if isinstance(v, str) and v.strip():
            return v.strip()
    return ""


def _load_json(path):
    try:
        with open(path, encoding="utf-8") as fh:
            return json.load(fh)
    except Exception:
        return None


def _find_registration(client_id_hash):
    """找 IdC 的 clientId/clientSecret:先按 {clientIdHash}.json(IDE),
    再兜底扫描 cache 目录里任何"含 clientId+clientSecret 且无 token"的注册文件(CLI 设备流)。"""
    if client_id_hash:
        reg = _load_json(os.path.join(CACHE_DIR, "%s.json" % client_id_hash))
        cid, cs = _first(reg, "clientId", "client_id"), _first(reg, "clientSecret", "client_secret")
        if cid and cs:
            return cid, cs
    try:
        names = sorted(os.listdir(CACHE_DIR))
    except OSError:
        names = []
    for name in names:
        if not name.endswith(".json"):
            continue
        reg = _load_json(os.path.join(CACHE_DIR, name))
        if not isinstance(reg, dict) or reg.get("accessToken") or reg.get("refreshToken"):
            continue  # 注册文件通常没有 access/refresh token
        cid, cs = _first(reg, "clientId", "client_id"), _first(reg, "clientSecret", "client_secret")
        if cid and cs:
            return cid, cs
    return "", ""


def read_credentials():
    # 优先级找 token 文件:IDE(kiro-auth-token.json) / CLI(device-sso-lsp-token.json);都在取最近修改的。
    candidates = [
        os.path.join(CACHE_DIR, n) for n in ("kiro-auth-token.json", "device-sso-lsp-token.json")
    ]
    found = [p for p in candidates if os.path.isfile(p)]
    if not found:
        die("在 %s 里没找到 kiro-auth-token.json 或 device-sso-lsp-token.json\n"
            "  → 先登录一次:Kiro IDE 登录,或 kiro-cli login --use-device-flow,再重试。" % CACHE_DIR)
    tok_path = max(found, key=os.path.getmtime)
    src_name = os.path.basename(tok_path)
    tok = _load_json(tok_path)
    if tok is None:
        die("无法解析 %s(不是合法 JSON)。" % src_name)

    refresh = _first(tok, "refreshToken", "refresh_token")
    if not refresh:
        die("%s 里没有 refreshToken(可能未登录成功)。" % src_name)

    is_cli = src_name == "device-sso-lsp-token.json"
    auth_method = _first(tok, "authMethod", "auth_method").lower()
    provider = _first(tok, "provider")
    client_id_hash = _first(tok, "clientIdHash", "client_id_hash")
    # external_idp(委托外部身份提供商,如 Microsoft Entra ID / Azure AD)按 authMethod / provider 判断,
    # 优先级高于 IdC 判断(external_idp 不走 clientIdHash 注册文件那一套)。
    is_external_idp = auth_method == "external_idp" or provider.lower() == "externalidp"
    # CLI 设备流一定是 IdC/BuilderId;IDE 则按 authMethod / clientIdHash 判断。
    is_idc = not is_external_idp and (
        is_cli or auth_method in ("idc", "builder-id", "builderid", "iam") or bool(client_id_hash)
    )

    if is_external_idp:
        token_endpoint = _first(tok, "tokenEndpoint", "token_endpoint")
        client_id = _first(tok, "clientId", "client_id")
        if not (token_endpoint and client_id):
            die("external_idp 登录但缺少 tokenEndpoint/clientId(token 文件字段不全)。\n"
                "  → 确认已通过外部 IdP(如 Microsoft Entra)完整登录一次。")
        creds = {
            "auth_method": "external_idp",
            "refresh_token": refresh,
            "client_id": client_id,
            "token_endpoint": token_endpoint,
        }
        for src, dst in (("accessToken", "access_token"), ("scopes", "scopes"), ("region", "region")):
            v = _first(tok, src)
            if v:
                creds[dst] = v
        client_secret = _first(tok, "clientSecret", "client_secret")
        if client_secret:
            creds["client_secret"] = client_secret
        kind = "external_idp(外部 IdP / 如 Microsoft Entra)"
        return creds, "%s(%s)" % (src_name, kind)

    creds = {"auth_method": "idc" if is_idc else "social", "refresh_token": refresh}
    for src, dst in (("accessToken", "access_token"), ("profileArn", "profile_arn"), ("region", "region")):
        v = _first(tok, src)
        if v:
            creds[dst] = v

    if is_idc:
        cid = _first(tok, "clientId", "client_id")
        cs = _first(tok, "clientSecret", "client_secret")
        if not (cid and cs):
            cid, cs = _find_registration(client_id_hash)
        if not (cid and cs):
            die("IdC 登录但没找到 clientId/clientSecret(token 文件与注册文件里都没有)。\n"
                "  → 确认登录已完成;IDE 登录会写 {clientIdHash}.json 注册文件。")
        creds["client_id"] = cid
        creds["client_secret"] = cs

    kind = ("idc/%s" % provider if is_idc else "social/%s" % (provider or "?"))
    return creds, "%s(%s)" % (src_name, kind)


def main():
    creds, label = read_credentials()
    print("读到本地 Kiro 凭据:%s;字段=%s" % (label, ",".join(sorted(creds.keys()))))

    st, resp = api("POST", "/api/v1/auth/login", body={"email": ADMIN_EMAIL, "password": ADMIN_PASSWORD})
    token = (resp.get("data") or {}).get("access_token")
    if not token:
        die("登录 sub2api 失败(%s):%s。检查 SUB2API_ADMIN_* 环境变量。" % (st, resp.get("message") or resp))

    # 有则更新、无则创建(按名字匹配)
    st, resp = api("GET", "/api/v1/admin/accounts?platform=kiro&page=1&page_size=100", token)
    items = (resp.get("data") or {}).get("items") or []
    existing = next((a for a in items if a.get("name") == ACCOUNT_NAME), None)

    payload = {
        "name": ACCOUNT_NAME,
        "platform": "kiro",
        "type": "oauth",
        "credentials": creds,
        "group_ids": [GROUP_ID],
        "confirm_mixed_channel_risk": True,
    }

    if existing:
        aid = existing["id"]
        st, resp = api("PUT", "/api/v1/admin/accounts/%d" % aid, token, payload)
        action = "更新"
    else:
        st, resp = api("POST", "/api/v1/admin/accounts", token, payload)
        action = "创建"

    if st != 200:
        die("%s账号失败(%s):%s" % (action, st, resp.get("message") or resp.get("code") or resp))

    d = resp.get("data") or {}
    print("✓ 已%s Kiro 账号:id=%s name=%s status=%s(凭据键:%s)" % (
        action, d.get("id"), d.get("name"), d.get("status"),
        ",".join(sorted((d.get("credentials") or {}).keys()))))
    print("  完成。下次刷新 token 只需再跑一遍本命令。")


if __name__ == "__main__":
    main()
