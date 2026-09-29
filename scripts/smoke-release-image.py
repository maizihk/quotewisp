#!/usr/bin/env python3
"""Smoke an immutable local image ID or registry digest with release metadata."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import uuid


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--commit", required=True)
    parser.add_argument("--build-time", required=True)
    args = parser.parse_args()
    root = Path(__file__).resolve().parent.parent
    spec = importlib.util.spec_from_file_location("drill", root / "scripts/verify-sqlite-release.py")
    drill = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(drill)
    docker, require = drill.docker, drill.require
    expected = {"version": args.version, "git_commit": args.commit, "build_time": args.build_time}
    image_id = json.loads(docker("image", "inspect", args.image))[0]["Id"]
    volume = "quotewisp-release-smoke-" + uuid.uuid4().hex
    containers = []
    docker("volume", "create", volume)
    try:
        previous_key = None
        for boot in range(2):
            name = volume + "-" + str(boot)
            docker("create", "--name", name, "--read-only", "--cap-drop=ALL",
                   "--security-opt=no-new-privileges",
                   "--tmpfs=/tmp:rw,noexec,nosuid,size=256m,uid=65532,gid=65532,mode=0700",
                   "-p", "127.0.0.1::8080", "-v", volume + ":/var/lib/quotewisp", image_id)
            containers.append(name)
            docker("start", name)
            client = drill.Client(name)
            drill.wait_for(lambda: client.request("/readyz")[0], "release image not ready")
            actual = json.loads(client.request("/version")[0])
            require(actual == expected, "release metadata mismatch: " + repr(actual))
            meta = json.loads(docker("inspect", name))[0]
            require(meta["Config"]["User"] == "65532:65532", "unexpected runtime user")
            env = {**os.environ, "BASE": client.base, "BASE_URL": client.base}
            for script in ("smoke.sh", "smoke-web.sh"):
                subprocess.run(["bash", str(root / "scripts" / script)], env=env, check=True)
            docker("stop", name)
            state, key = drill.inspect_backup(drill.archive(name))
            require(state == {"schema": 5, "sentences": 1, "admins": 0}, "unexpected persisted state")
            require(previous_key is None or key == previous_key, "secret changed after recreation")
            previous_key = key
        print(json.dumps({"result": "passed", "image": image_id, "metadata": expected,
                          "boots": 2, "database": state}, indent=2), flush=True)
    finally:
        for name in reversed(containers):
            docker("rm", "-f", name, check=False)
        docker("volume", "rm", volume, check=False)


if __name__ == "__main__":
    main()
