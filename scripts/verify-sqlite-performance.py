#!/usr/bin/env python3
"""Run isolated SQLite k6 traffic with three 100k-row admin imports.

Requires locally available application/k6 images, Go, Docker and Python 3.
Artifacts contain only synthetic data and measurements. No production targets
are accepted. Default run: 60s warmup + 600s at 1000 requests/second.
"""
import argparse
import csv
import importlib.util
import json
import os
from pathlib import Path
import platform
import re
import secrets
import subprocess
import threading
import time
import urllib.parse
import uuid

spec = importlib.util.spec_from_file_location("drill", Path(__file__).with_name("verify-sqlite-release.py"))
drill = importlib.util.module_from_spec(spec)
spec.loader.exec_module(drill)
docker, require = drill.docker, drill.require


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", required=True)
    parser.add_argument("--k6-image", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--tmpfs-mib", type=int, default=256)
    parser.add_argument("--vus", type=int, default=128, help="preallocated VUs per k6 scenario")
    parser.add_argument("--preflight-only", action="store_true", help="validate all import batches before starting the long load test")
    args = parser.parse_args()
    require(args.tmpfs_mib > 0, "tmpfs size must be positive")
    require(1 <= args.vus <= 256, "VUs must be within 1..256")
    output = Path(args.output).resolve()
    output.mkdir(mode=0o700, parents=True, exist_ok=False)
    root = Path(__file__).resolve().parent.parent
    cpus = sorted(os.sched_getaffinity(0))
    require(len(cpus) >= 4, "need four CPUs for isolated application, orchestration and load generator")
    os.sched_setaffinity(0, {cpus[1]})
    prefix = "quotewisp-perf-" + uuid.uuid4().hex[:12]
    app, load, volume = prefix + "-app", prefix + "-load", prefix + "-data"
    stop = threading.Event()
    report = {"started_utc": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
              "os": platform.platform(), "cpus": cpus, "application_cpu": cpus[0],
              "load_cpus": cpus[-2:], "memory_limit_bytes": 1 << 30,
              "preallocated_vus": args.vus, "max_vus": 256, "gomemlimit": "768MiB", "tmpfs_limit_bytes": args.tmpfs_mib << 20, "rps": 1000, "warmup_seconds": 60,
              "measurement_seconds": 600, "import_window_measured_seconds": [60, 300],
              "imports": []}
    load_process = None
    sampler = None
    try:
        report["cpu_model"] = next(line.split(":", 1)[1].strip() for line in Path("/proc/cpuinfo").read_text().splitlines() if line.startswith("model name"))
        report["host_go_version"] = subprocess.check_output(["go", "version"], text=True).strip()
        report["image_id"] = json.loads(docker("image", "inspect", args.image))[0]["Id"]
        report["k6_version"] = docker("run", "--rm", "--network=none", args.k6_image, "version").decode().strip()
        docker("volume", "create", volume)
        docker("run", "-d", "--name", app, "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges",
               f"--tmpfs=/tmp:rw,noexec,nosuid,size={args.tmpfs_mib}m,uid=65532,gid=65532,mode=0700",
               "--cpus=1", "--cpuset-cpus=" + str(cpus[0]), "--memory=1g", "--memory-swap=1g",
               "--log-opt=max-size=10m", "--log-opt=max-file=2", "-p", "127.0.0.1::8080",
               "-e", "COOKIE_SECURE=false", "-e", "GOMEMLIMIT=768MiB", "-e", "IMPORT_MAX_UPLOAD_BYTES=134217728",
               "-e", "IMPORT_TIMEOUT=180s", "-e", "SNAPSHOT_LOAD_TIMEOUT=120s", "-e", "SNAPSHOT_POLL_INTERVAL=5s",
               "-v", volume + ":/var/lib/quotewisp", args.image)
        client = drill.Client(app)
        drill.wait_for(lambda: client.request("/readyz")[0], "application did not become ready")
        password = secrets.token_urlsafe(24)
        docker("exec", "-i", app, "/sentence-api", "web", "admin", "create", "--username", "drilladmin", "--password-stdin", data=(password + "\n").encode())
        client.login(password)
        # Verify the actual CLI cannot initialize a second administrator.
        duplicate = subprocess.run(["docker", "exec", "-i", app, "/sentence-api", "web", "admin", "create",
                                    "--username", "secondadmin", "--password-stdin"], input=(password + "\n").encode(), capture_output=True)
        require(duplicate.returncode != 0 and b"already initialized" in duplicate.stderr, "repeat CLI initialization accepted")
        report["repeat_cli_initialization_rejected"] = True

        def upload(count, start_id):
            generated = subprocess.check_output(["go", "run", "scripts/generate-fixture.go", "-count", str(count), "-start-id", str(start_id), "-seed", "20260918"], cwd=root)
            page, _ = client.request("/admin/imports")
            csrf = drill.token(page, "csrf_token")
            boundary = "perf-" + uuid.uuid4().hex
            body = (f'--{boundary}\r\nContent-Disposition: form-data; name="csrf_token"\r\n\r\n{csrf}\r\n'
                    f'--{boundary}\r\nContent-Disposition: form-data; name="format"\r\n\r\nnative\r\n'
                    f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="performance.json"\r\nContent-Type: application/json\r\n\r\n').encode() + generated + f'\r\n--{boundary}--\r\n'.encode()
            started = time.monotonic()
            _, headers = client.request("/admin/imports", body, "multipart/form-data; boundary=" + boundary, 303)
            location = headers["Location"]
            def page_has(text):
                page, _ = client.request(location)
                if 'status-badge">失败' in page or 'status-badge">刷新失败' in page:
                    error = re.search(r'<div class="flash">(.*?)</div>', page)
                    raise ValueError(error.group(1) if error else "import failed")
                return text in page
            drill.wait_for(lambda: page_has('确认导入'), "preview failed", 190)
            preview_time = time.monotonic() - started
            page, _ = client.request(location)
            confirmed = time.monotonic()
            client.form(location + "/confirm", {"csrf_token": csrf, "digest": drill.token(page, "digest")})
            drill.wait_for(lambda: page_has('API 与前台快照刷新已完成'), "import failed", 190)
            complete = time.monotonic()
            # Confirm the batch's last row is visible, including the >1000-codepoint fixture.
            last_id = f"00000000-0000-4000-8000-{start_id + count - 1:012x}"
            client.request("/api/v1/sentences/" + last_id)
            result = {"rows": count, "start_id": start_id, "file_bytes": len(generated),
                      "upload_preview_seconds": preview_time, "commit_refresh_seconds": complete - confirmed,
                      "started_after_load_seconds": started - load_started if load_process else None,
                      "completed_after_load_seconds": complete - load_started if load_process else None}
            report["imports"].append(result)
            print("IMPORT " + json.dumps(result), flush=True)

        load_started = 0
        upload(12000, 1)
        if args.preflight_only:
            for start_id in (12001, 112001, 212001):
                upload(100000, start_id)
            docker("stop", app)
            report["database"], _ = drill.inspect_backup(drill.archive(app))
            require(report["database"]["sentences"] == 312001, "final row count incorrect")
            report["result"] = "preflight_passed"
            print("RESULT " + json.dumps(report, ensure_ascii=False), flush=True)
            return
        pid = json.loads(docker("inspect", app))[0]["State"]["Pid"]
        cgroup = Path("/sys/fs/cgroup") / Path(f"/proc/{pid}/cgroup").read_text().strip().split("::", 1)[1].lstrip("/")
        epoch = time.monotonic()

        def sample():
            with (output / "memory.csv").open("w") as file:
                writer = csv.writer(file, lineterminator="\n")
                writer.writerow(["elapsed", "rss_bytes", "cgroup_current_bytes", "cgroup_peak_bytes"])
                while not stop.is_set():
                    try:
                        status = Path(f"/proc/{pid}/status").read_text()
                        rss = int(next(line.split()[1] for line in status.splitlines() if line.startswith("VmRSS:"))) * 1024
                        writer.writerow([round(time.monotonic() - epoch, 3), rss, (cgroup / "memory.current").read_text().strip(), (cgroup / "memory.peak").read_text().strip()])
                        file.flush()
                    except OSError:
                        break
                    stop.wait(1)
        sampler = threading.Thread(target=sample)
        sampler.start()
        # Copy script into an isolated k6 container; no writable host mount is needed.
        docker("create", "--name", load, "--network=host", "--cpuset-cpus=" + ','.join(map(str, cpus[-2:])),
               "-e", "BASE_URL=" + client.base, "-e", f"PREALLOCATED_VUS={args.vus}", "-e", "MAX_VUS=256",
               "-e", "MIN_REQUESTS=600000", "-e", "IMPORT_WINDOW_START=60", "-e", "IMPORT_WINDOW_END=300",
               args.k6_image, "run", "--summary-export=/tmp/summary.json", "/tmp/performance.js")
        docker("cp", str(root / "scripts/performance.js"), load + ":/tmp/performance.js")
        log = (output / "k6.log").open("wb")
        load_started = time.monotonic()
        load_process = subprocess.Popen(["docker", "start", "-a", load], stdout=log, stderr=subprocess.STDOUT)
        # All batch activity should fall inside the explicit 60..300s measured window.
        for offset, start_id in ((130, 12001), (205, 112001), (280, 212001)):
            while time.monotonic() - load_started < offset:
                require(load_process.poll() is None, "k6 stopped early")
                time.sleep(1)
            upload(100000, start_id)
        while load_process.poll() is None:
            time.sleep(1)
        log.close()
        report["k6_exit_code"] = json.loads(docker("inspect", load))[0]["State"]["ExitCode"]
        docker("cp", load + ":/tmp/summary.json", str(output / "summary.json"))
        report["cgroup_memory_events"] = (cgroup / "memory.events").read_text()
        report["cgroup_memory_peak_bytes"] = int((cgroup / "memory.peak").read_text())
        metrics, _ = client.request("/metrics")
        (output / "metrics.prom").write_text(metrics)
        docker("stop", app)
        snapshot = drill.archive(app)
        report["database"], _ = drill.inspect_backup(snapshot)
        require(report["database"]["sentences"] == 312001, "final row count incorrect")
        require(all(item["started_after_load_seconds"] >= 120 and item["completed_after_load_seconds"] <= 360
                    for item in report["imports"][1:]), "imports outside measured import window")
        report["result"] = "passed" if report["k6_exit_code"] == 0 else "thresholds_failed"
        print("RESULT " + json.dumps(report, ensure_ascii=False), flush=True)
        if report["k6_exit_code"] != 0:
            raise SystemExit(1)
    except Exception as error:
        report["result"] = "aborted"
        report["error"] = str(error)
        raise
    finally:
        diagnostic = subprocess.run(["docker", "logs", app], stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        lines = []
        for line in diagnostic.stdout.decode(errors="replace").splitlines():
            try:
                event = json.loads(line)
                if event.get("msg") != "http_request":
                    lines.append(line)
            except json.JSONDecodeError:
                continue
        (output / "application-events.jsonl").write_text("\n".join(lines) + "\n")
        stop.set()
        if sampler:
            sampler.join(timeout=5)
        docker("rm", "-f", load, check=False)
        docker("rm", "-f", app, check=False)
        docker("volume", "rm", volume, check=False)
        if load_process and load_process.poll() is None:
            load_process.wait(timeout=10)
        (output / "report.json").write_text(json.dumps(report, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
