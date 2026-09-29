#!/usr/bin/env python3
"""Isolated Docker upgrade, admin import, offline backup/restore and rollback drill.

Requires Python 3 and two locally built images. Creates only uniquely named test
containers/volumes, binds random loopback ports, and removes its resources on exit.
"""
import argparse
import hashlib
import http.cookiejar
import io
import json
import re
import secrets
import sqlite3
import subprocess
import tarfile
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid


def docker(*args, data=None, check=True):
    return subprocess.run(["docker", *args], input=data, stdout=subprocess.PIPE,
                          stderr=subprocess.PIPE, check=check).stdout


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class Client:
    def __init__(self, container):
        addr = docker("port", container, "8080/tcp").decode().strip()
        self.base = "http://" + addr
        self.opener = urllib.request.build_opener(
            urllib.request.ProxyHandler({}), NoRedirect(),
            urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))

    def request(self, path, data=None, content_type=None, expected=200):
        headers = {"Content-Type": content_type} if content_type else {}
        req = urllib.request.Request(self.base + path, data=data, headers=headers)
        try:
            response = self.opener.open(req, timeout=10)
        except urllib.error.HTTPError as err:
            response = err
        with response:
            raw = response.read()
            require(response.code == expected,
                    f"{path}: HTTP {response.code}, expected {expected}")
            return raw.decode(), response.headers

    def form(self, path, values, expected=303):
        return self.request(path, urllib.parse.urlencode(values).encode(),
                            "application/x-www-form-urlencoded", expected)

    def login(self, password):
        page, _ = self.request("/admin/login")
        self.form("/admin/login", {"form_token": token(page, "form_token"),
                                 "username": "drilladmin", "password": password})

    def dataset(self):
        body, _ = self.request("/dataset/sentences.json")
        return json.loads(body)


def token(page, name):
    found = re.search(r'name="' + re.escape(name) + r'" value="([^"]+)"', page)
    require(found is not None, "missing form field " + name)
    return found.group(1)


def wait_for(probe, message, seconds=45):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        try:
            if probe():
                return
        except (OSError, urllib.error.URLError, RuntimeError):
            pass
        time.sleep(0.1)
    raise RuntimeError(message)


def archive(container):
    # Only call on stopped containers; includes database, WAL/SHM and secret.
    state = json.loads(docker("inspect", container))[0]["State"]
    require(not state["Running"], "backup requires a stopped application")
    return docker("cp", container + ":/var/lib/quotewisp/.", "-")


def inspect_backup(raw):
    with tarfile.open(fileobj=io.BytesIO(raw)) as tar:
        members = {m.name.rsplit("/", 1)[-1]: m for m in tar.getmembers() if m.isfile()}
        require("quotewisp.db" in members and "web-secret.key" in members, "incomplete backup")
        require(members["quotewisp.db"].uid == 65532, "database not owned by runtime UID")
        require(members["web-secret.key"].mode & 0o077 == 0, "secret permissions too broad")
        require(not any("/uploads/" in m.name for m in members.values()), "completed upload file retained")
        secret_hash = hashlib.sha256(tar.extractfile(members["web-secret.key"]).read()).hexdigest()
        with tempfile.TemporaryDirectory(prefix="quotewisp-inspect-") as directory:
            # Fixed filenames only; never extract arbitrary archive paths.
            for name in ("quotewisp.db", "quotewisp.db-wal", "quotewisp.db-shm"):
                if name in members:
                    with open(directory + "/" + name, "wb") as output:
                        output.write(tar.extractfile(members[name]).read())
            with sqlite3.connect(directory + "/quotewisp.db") as db:
                require(db.execute("PRAGMA integrity_check").fetchone()[0] == "ok", "corrupt backup")
                require(not db.execute("PRAGMA foreign_key_check").fetchall(), "invalid foreign keys")
                version = db.execute("SELECT version FROM schema_migrations").fetchone()[0]
                count = db.execute("SELECT count(*) FROM sentences").fetchone()[0]
                admins = db.execute("SELECT count(*) FROM admin_users").fetchone()[0]
        return {"schema": version, "sentences": count, "admins": admins}, secret_hash


def import_file(client, fmt, sentence_id, count=1):
    page, _ = client.request("/admin/imports")
    csrf = token(page, "csrf_token")
    if fmt == "native":
        payload = {"categories": [{"code": "drill", "name": "演练", "sort_order": 0}],
                   "sentences": [{"uuid": sentence_id if i == 0 else str(uuid.uuid4()), "category": "drill", "content": "离线恢复演练语句" + "测试" * 80} for i in range(count)]}
    else:
        payload = [{"uuid": sentence_id, "type": "drill", "hitokoto": "适配导入演练语句",
                    "from": "本地测试", "from_who": None}]
    boundary = "drill-" + uuid.uuid4().hex
    fields = [("csrf_token", csrf), ("format", fmt)]
    chunks = [f'--{boundary}\r\nContent-Disposition: form-data; name="{k}"\r\n\r\n{v}\r\n'
              for k, v in fields]
    chunks.append(f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="data.json"\r\n'
                  'Content-Type: application/json\r\n\r\n' + json.dumps(payload) + '\r\n')
    chunks.append(f'--{boundary}--\r\n')
    _, headers = client.request("/admin/imports", ''.join(chunks).encode(),
                                "multipart/form-data; boundary=" + boundary, 303)
    location = headers["Location"]
    wait_for(lambda: '确认导入' in client.request(location)[0], "preview did not finish")
    preview, _ = client.request(location)
    values = {"csrf_token": csrf, "digest": token(preview, "digest")}
    client.form(location + "/confirm", {**values, "csrf_token": "bad"}, 403)
    client.form(location + "/confirm", values)
    wait_for(lambda: 'API 与前台快照刷新已完成' in client.request(location)[0], "import did not finish")
    client.form(location + "/confirm", values, 409)
    client.request("/api/v1/sentences/" + sentence_id)
    return location


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", required=True)
    parser.add_argument("--baseline-image", required=True, help="SQLite schema 4 image")
    args = parser.parse_args()
    prefix = "quotewisp-drill-" + uuid.uuid4().hex[:12]
    containers, volumes = [], []
    password = secrets.token_urlsafe(24)
    report = {}

    def volume():
        name = prefix + "-v" + str(len(volumes))
        docker("volume", "create", name)
        volumes.append(name)
        return name

    def create(image, vol):
        name = prefix + "-c" + str(len(containers))
        docker("create", "--name", name, "--read-only", "--cap-drop=ALL",
               "--security-opt=no-new-privileges",
               "--tmpfs=/tmp:rw,noexec,nosuid,size=256m,uid=65532,gid=65532,mode=0700", "-p", "127.0.0.1::8080",
               "-e", "COOKIE_SECURE=false", "-v", vol + ":/var/lib/quotewisp", image)
        containers.append(name)
        return name

    def start(name):
        docker("start", name)
        client = Client(name)
        wait_for(lambda: client.request("/readyz")[0], "container failed readiness")
        meta = json.loads(docker("inspect", name))[0]
        require(meta["Config"]["User"] == "65532:65532" and meta["HostConfig"]["ReadonlyRootfs"],
                "container security configuration changed")
        return client

    def settings(client):
        page, _ = client.request("/admin/settings")
        require('恢复演练站点' in page and '保留站点设置' in page, "site settings lost")

    try:
        for key, image in (("image", args.image), ("baseline_image", args.baseline_image)):
            report[key] = json.loads(docker("image", "inspect", image))[0]["Id"]
        fresh = create(args.image, volume())
        fresh_client = start(fresh)
        fresh_client.request("/api/v1")
        docker("stop", fresh)
        fresh_info, _ = inspect_backup(archive(fresh))
        require(fresh_info["schema"] == 5 and fresh_info["sentences"] == 1, "fresh initialization failed")
        report["fresh"] = fresh_info
        print("PASS: final image initializes schema 5 and seed on an empty named volume", flush=True)
        original_volume = volume()
        old = create(args.baseline_image, original_volume)
        client = start(old)
        docker("exec", "-i", old, "/sentence-api", "web", "admin", "create",
               "--username", "drilladmin", "--password-stdin", data=(password + "\n").encode())
        client.login(password)
        page, _ = client.request("/admin/settings")
        client.form("/admin/settings", {"csrf_token": token(page, "csrf_token"),
                    "site_name": "恢复演练站点", "english_name": "Restore Drill",
                    "slogan": "保留站点设置", "contact": "drill@example.invalid"})
        settings(client)
        original_data = client.dataset()
        docker("stop", old)
        before = archive(old)
        report["before"], key_before = inspect_backup(before)
        require(report["before"]["schema"] == 4, "baseline must use schema 4")
        print("PASS: baseline initialization, admin login, settings, offline schema 4 backup", flush=True)

        current = create(args.image, original_volume)
        client = start(current)
        client.login(password)
        settings(client)
        require(client.dataset() == original_data, "upgrade changed existing dataset")
        jobs = [import_file(client, fmt, str(uuid.uuid4()), 12000 if fmt == "native" else 1) for fmt in ("native", "hitokoto")]
        imported_data = client.dataset()
        client.request("/api/v1?categories=drill")
        docker("stop", current)
        after = archive(current)
        report["after"], key_after = inspect_backup(after)
        require(report["after"]["schema"] == 5, "upgrade did not apply schema 5")
        require(report["after"]["sentences"] == report["before"]["sentences"] + 12001, "unexpected imports")
        require(key_before == key_after, "upgrade replaced secret")
        require(report["after"]["admins"] == report["before"]["admins"] == 1, "administrator lost")
        print("PASS: schema 4→5 upgrade, both imports, CSRF, duplicate confirm, API refresh", flush=True)

        restored_volume = volume()
        restored = create(args.image, restored_volume)
        docker("cp", "-a", "-", restored + ":/var/lib/quotewisp", data=after)
        client = start(restored)
        client.login(password)
        settings(client)
        require(client.dataset() == imported_data, "restored dataset differs")
        for job in jobs:
            require('API 与前台快照刷新已完成' in client.request(job)[0], "import receipt lost")
        docker("stop", restored)
        restored_info, restored_key = inspect_backup(archive(restored))
        require(restored_info == report["after"] and restored_key == key_after, "restored state differs")
        # Recreate the application container while reusing the restored named volume.
        recreated = create(args.image, restored_volume)
        client = start(recreated)
        client.login(password)
        require(client.dataset() == imported_data, "container recreation lost dataset")
        docker("stop", recreated)
        print("PASS: new-volume restore, admin/settings/secret/receipts preserved, container recreation", flush=True)

        incompatible = create(args.baseline_image, restored_volume)
        docker("start", incompatible)
        wait_for(lambda: not json.loads(docker("inspect", incompatible))[0]["State"]["Running"],
                 "old binary accepted newer schema", 15)
        state = json.loads(docker("inspect", incompatible))[0]["State"]
        require(state["ExitCode"] != 0, "old binary exited successfully on new schema")
        rejected_info, _ = inspect_backup(archive(incompatible))
        require(rejected_info == report["after"], "rejected downgrade changed data")
        rollback = create(args.baseline_image, volume())
        docker("cp", "-a", "-", rollback + ":/var/lib/quotewisp", data=before)
        client = start(rollback)
        client.login(password)
        settings(client)
        require(client.dataset() == original_data, "rollback differs from pre-upgrade backup")
        docker("stop", rollback)
        rollback_info, rollback_key = inspect_backup(archive(rollback))
        require(rollback_info == report["before"] and rollback_key == key_before, "rollback state differs")
        print("PASS: newer-schema refusal and pre-upgrade backup + matching-image rollback", flush=True)
        report["result"] = "passed"
        print(json.dumps(report, ensure_ascii=False, indent=2))
    finally:
        for name in reversed(containers):
            docker("rm", "-f", name, check=False)
        for name in reversed(volumes):
            docker("volume", "rm", name, check=False)


if __name__ == "__main__":
    main()
