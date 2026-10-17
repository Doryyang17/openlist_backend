#!/usr/bin/env python3
"""Exercise favorites through isolated QA HTTP APIs, never a production DB."""
import json
import secrets
import time
import urllib.request
from pathlib import Path

RUNTIME = Path.home() / "openlist-dev"
ORIGIN = "http://127.0.0.1:5245"
PROXY = "http://127.0.0.1:5173"


def request(route, body=None, token=None, origin=ORIGIN):
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = token
    req = urllib.request.Request(origin + "/api/" + route,
        data=None if body is None else json.dumps(body).encode(), headers=headers)
    with urllib.request.urlopen(req, timeout=15) as response:
        return json.load(response)


def ok(route, body=None, token=None, origin=ORIGIN):
    result = request(route, body, token, origin)
    assert result["code"] == 200, (route, result["code"], result.get("data", {}).get("reason") if isinstance(result.get("data"), dict) else "")
    return result.get("data")


def main():
    config = json.loads((RUNTIME / "data/config.json").read_text())
    assert config["scheme"]["address"] == "127.0.0.1" and config["scheme"]["http_port"] == 5245
    assert Path(config["database"]["db_file"]).resolve().is_relative_to(RUNTIME.resolve())
    credentials = json.loads((RUNTIME / "credentials.json").read_text())
    admin = ok("auth/login", {"username": credentials["username"], "password": credentials["password"]})["token"]
    assert ok("favorites/capabilities", token=admin)["version"] == 1
    assert ok("favorites/capabilities", token=admin, origin=PROXY)["version"] == 1
    assert request("favorites/list", {}, None)["code"] in (401, 403)
    stamp = "favorites-qa-" + str(time.time_ns())
    folder = RUNTIME / "sample-files" / stamp
    folder.mkdir(parents=True)
    (folder / "资料").mkdir()
    (folder / "protected").mkdir()
    (folder / "other").mkdir()
    for name in ("说明.txt", "100%_ #?.txt", "other/说明.txt", "protected/secret.txt"):
        (folder / name).write_text("收藏 QA 合成文件。\n", encoding="utf-8")
    root = "/sandbox/" + stamp
    users = []
    for suffix in ("a", "b"):
        username, password = stamp + suffix, secrets.token_urlsafe(24)
        ok("admin/user/create", {"username": username, "password": password, "base_path": root, "role": 0, "permission": 0}, admin)
        records = ok("admin/user/list?page=1&per_page=100", token=admin)["content"]
        user = next(u for u in records if u["username"] == username)
        token = ok("auth/login", {"username": username, "password": password})["token"]
        users.append((user, token, password))
    a, at, ap = users[0]; b, bt, bp = users[1]
    item = ok("favorites/add", {"path": "/说明.txt"}, at)
    repeated = ok("favorites/add", {"path": "/说明.txt"}, at)
    assert repeated["id"] == item["id"] and repeated["created_at"] == item["created_at"]
    special = ok("favorites/add", {"path": "/100%_ #?.txt"}, at)
    directory = ok("favorites/add", {"path": "/资料"}, at)
    assert directory["is_dir"] and not item["is_dir"]
    assert ok("favorites/list", {"q": "%_"}, at)["total"] == 1
    assert ok("favorites/list", {"type": "folder"}, at)["total"] == 1
    assert ok("favorites/list", {}, bt)["total"] == 0
    assert request("favorites/resolve", {"id": item["id"], "action": "open"}, bt)["code"] == 404
    ok("favorites/delete", {"id": item["id"]}, bt)
    assert ok("favorites/resolve", {"id": item["id"], "action": "open"}, at)["path"] == "/说明.txt"
    assert ok("favorites/resolve", {"id": item["id"], "action": "parent"}, at)["path"] == "/"
    assert ok("favorites/list", {}, at, PROXY)["total"] == 3
    assert ok("favorites/status", {"paths": ["/说明.txt", "/missing"]}, at)[0]["id"] == item["id"]
    for invalid in ("/", "/../outside", "/@s/share", "relative"):
        assert request("favorites/add", {"path": invalid}, at)["code"] == 400
    assert request("favorites/status", {"paths": ["/说明.txt"] * 101}, at)["code"] == 400
    assert request("favorites/list", {"per_page": 1000}, at)["code"] == 400
    # Only mutate accounts created by this run.
    ok("admin/user/update", {**a, "base_path": root + "/other", "password": ""}, admin)
    at = ok("auth/login", {"username": a["username"], "password": ap})["token"]
    assert request("favorites/resolve", {"id": item["id"], "action": "open"}, at)["data"]["reason"] == "outside_root"
    assert all(i["path"] is None for i in ok("favorites/list", {}, at)["content"])
    ok("admin/user/update", {**a, "password": ""}, admin)
    at = ok("auth/login", {"username": a["username"], "password": ap})["token"]
    secret = secrets.token_urlsafe(12)
    ok("admin/meta/create", {"path": root + "/protected", "password": secret, "p_sub": True}, admin)
    assert request("favorites/add", {"path": "/protected/secret.txt"}, at)["data"]["reason"] == "password_required"
    protected = ok("favorites/add", {"path": "/protected/secret.txt", "password": secret}, at)
    assert request("favorites/resolve", {"id": protected["id"], "action": "open"}, at)["data"]["reason"] == "password_required"
    assert ok("favorites/resolve", {"id": protected["id"], "action": "open", "password": secret}, at)["path"] == "/protected/secret.txt"
    ok("admin/meta/create", {"path": root + "/资料", "read_users": [b["id"]], "read_users_sub": True}, admin)
    assert request("favorites/resolve", {"id": directory["id"], "action": "open"}, at)["data"]["reason"] == "forbidden"
    ok("favorites/delete", {"id": directory["id"]}, at)
    ok("favorites/delete", {"id": item["id"]}, at)
    newer = ok("favorites/add", {"path": "/说明.txt"}, at)
    ok("favorites/delete", {"id": item["id"]}, at)
    assert ok("favorites/resolve", {"id": newer["id"], "action": "open"}, at)["path"] == "/说明.txt"
    for i in range(51):
        name = f"分页-{i:02d}.txt"
        (folder / name).write_text("分页合成数据\n")
        ok("favorites/add", {"path": "/" + name}, at)
    page1 = ok("favorites/list", {"q": "分页", "page": 1, "per_page": 50}, at)
    page2 = ok("favorites/list", {"q": "分页", "page": 2, "per_page": 50}, at)
    assert page1["total"] == 51 and len(page1["content"]) == 50 and len(page2["content"]) == 1
    assert not ({i["id"] for i in page1["content"]} & {i["id"] for i in page2["content"]})
    ok("admin/user/delete?id=" + str(a["id"]), {}, admin)
    assert request("favorites/list", {}, at)["code"] == 401
    # Keep a small, readable admin fixture for browser acceptance, not private files.
    for p in (root + "/说明.txt", root + "/100%_ #?.txt", root + "/other"):
        ok("favorites/add", {"path": p}, admin)
    state = {"cookies": [], "origins": [{"origin": PROXY, "localStorage": [{"name": "token", "value": admin}]}]}
    auth_path = RUNTIME / "playwright-auth.json"
    auth_path.write_text(json.dumps(state)); auth_path.chmod(0o600)
    (RUNTIME / "favorites-qa-fixture.json").write_text(json.dumps({"root": root, "reader_id": b["id"]}))
    print("PASS: direct/proxy connectivity, guest rejection, account isolation, idempotence, file/folder, literal filtering, paging, root change, password, revoked permission, account deletion, stale-delete safety.")
    print("Browser fixture ready; private storage state saved under QA runtime (credentials not printed).")


if __name__ == "__main__":
    main()
