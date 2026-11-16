#!/usr/bin/env python3
"""Compares OpenAPI Mocker with WireMock, Prism and Mockoon CLI.

Measures, for each tool serving the same Petstore:

  * start time: from spawning the process to the first 200 answer;
  * memory: resident set size (RSS) of the whole process tree, idle and the
    peak during load;
  * throughput and latency: requests per second, p50 and p99 for GET /pets/1,
    from the `hey` load generator, at several concurrency levels.

Python standard library only. See docs/benchmark.md for the method, the caveats
and how to read the numbers; benchmark/run-all.sh installs everything and runs
this script.

Example:
    python3 benchmark/run.py --out benchmark/results/mine.json \
        --openapi-mocker ./openapi-mocker --hey ./hey \
        --wiremock-jar wiremock-standalone.jar \
        --prism node_modules/.bin/prism --mockoon node_modules/.bin/mockoon-cli
"""
import argparse
import http.client
import json
import os
import platform
import re
import shutil
import signal
import socket
import statistics
import subprocess
import sys
import tempfile
import threading
import time

HERE = os.path.dirname(os.path.abspath(__file__))
SPEC = os.path.join(HERE, "..", "examples", "petstore.yaml")
WIREMOCK_ROOT = os.path.join(HERE, "wiremock")
PROBE_PATH = "/pets/1"


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def tools(args):
    """The tools to compare: name -> (version command, start command builder)."""
    spec = os.path.abspath(SPEC)
    table = {}
    if args.openapi_mocker:
        table["OpenAPI Mocker"] = dict(
            version=[args.openapi_mocker, "version"],
            start=lambda port: [args.openapi_mocker, "serve", spec, "--port", str(port)],
            env={},
        )
    if args.wiremock_jar:
        table["WireMock"] = dict(
            version=["java", "-jar", args.wiremock_jar, "--version"],
            start=lambda port: ["java", "-jar", args.wiremock_jar, "--port", str(port),
                                "--root-dir", os.path.abspath(WIREMOCK_ROOT), "--disable-banner"],
            env={},
        )
    if args.prism:
        table["Prism"] = dict(
            version=[args.prism, "--version"],
            start=lambda port: [args.prism, "mock", "-p", str(port), spec],
            env={},
        )
    if args.mockoon:
        table["Mockoon CLI"] = dict(
            version=[args.mockoon, "--version"],
            start=lambda port: [args.mockoon, "start", "--data", spec, "--port", str(port)],
            env={},
        )
    return table


# ---------- process helpers ----------

def children_of(pid):
    """pid and all its descendants, from /proc."""
    parent = {}
    for entry in os.listdir("/proc"):
        if not entry.isdigit():
            continue
        try:
            with open(f"/proc/{entry}/stat") as f:
                stat = f.read()
            # the command name may hold spaces and parentheses: cut after the last ")".
            fields = stat[stat.rindex(")") + 2:].split()
            parent[int(entry)] = int(fields[1])
        except (OSError, ValueError, IndexError):
            continue
    found, frontier = {pid}, [pid]
    while frontier:
        cur = frontier.pop()
        for child, par in parent.items():
            if par == cur and child not in found:
                found.add(child)
                frontier.append(child)
    return found


def rss_kib(pid):
    """Resident memory of the process tree, in KiB."""
    total = 0
    for p in children_of(pid):
        try:
            with open(f"/proc/{p}/status") as f:
                for line in f:
                    if line.startswith("VmRSS:"):
                        total += int(line.split()[1])
                        break
        except OSError:
            continue
    return total


class Server:
    def __init__(self, name, cfg, logdir):
        self.name = name
        self.port = free_port()
        self.log = open(os.path.join(logdir, re.sub(r"\W+", "_", name) + ".log"), "ab")
        self.t0 = time.perf_counter()
        env = dict(os.environ)
        env.pop("PORT", None)
        env.update(cfg["env"])
        self.proc = subprocess.Popen(cfg["start"](self.port), stdout=self.log, stderr=self.log,
                                     env=env, start_new_session=True, cwd=logdir)

    def wait_ready(self, timeout=120.0):
        """Seconds from spawn to the first 200 for the probe path."""
        deadline = time.perf_counter() + timeout
        while time.perf_counter() < deadline:
            if self.proc.poll() is not None:
                raise RuntimeError(f"{self.name} exited early with {self.proc.returncode}")
            try:
                conn = http.client.HTTPConnection("127.0.0.1", self.port, timeout=1)
                conn.request("GET", PROBE_PATH)
                resp = conn.getresponse()
                body = resp.read()
                conn.close()
                if resp.status == 200:
                    self.probe_bytes = len(body)
                    return time.perf_counter() - self.t0
            except OSError:
                pass
            time.sleep(0.005)
        raise RuntimeError(f"{self.name} did not answer within {timeout} s")

    def stop(self):
        if self.proc.poll() is None:
            try:
                os.killpg(self.proc.pid, signal.SIGTERM)
                self.proc.wait(timeout=15)
            except (ProcessLookupError, subprocess.TimeoutExpired):
                pass
        try:
            os.killpg(self.proc.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        self.log.close()


class PeakSampler(threading.Thread):
    """Samples the RSS of a process tree until stopped."""

    def __init__(self, pid, every=0.25):
        super().__init__(daemon=True)
        self.pid, self.every, self.peak = pid, every, 0
        self.stop_flag = threading.Event()

    def run(self):
        while not self.stop_flag.is_set():
            self.peak = max(self.peak, rss_kib(self.pid))
            self.stop_flag.wait(self.every)

    def finish(self):
        self.stop_flag.set()
        self.join()
        return self.peak


# ---------- measurements ----------

def hey(args, url, seconds, conns):
    cmd = [args.hey, "-z", f"{seconds}s", "-c", str(conns), url]
    # Talk to the server directly: a proxy from the environment would be measured instead of it.
    env = {k: v for k, v in os.environ.items() if k.lower() not in ("http_proxy", "https_proxy", "all_proxy")}
    env["NO_PROXY"] = env["no_proxy"] = "127.0.0.1,localhost"
    out = subprocess.run(cmd, capture_output=True, text=True, timeout=seconds + 60, env=env).stdout
    rps = float(re.search(r"Requests/sec:\s+([\d.]+)", out).group(1))
    lat = {int(m.group(1)): float(m.group(2)) * 1000 for m in re.finditer(r"^\s+(\d+)%+ in ([\d.]+) secs", out, re.M)}
    statuses = {int(m.group(1)): int(m.group(2)) for m in re.finditer(r"\[(\d+)\]\s+(\d+) responses", out)}
    errors = sum(n for code, n in statuses.items() if code != 200)
    m = re.search(r"Error distribution:\n((?:\s+\[\d+\].*\n?)+)", out)
    errors += sum(int(x) for x in re.findall(r"\[(\d+)\]", m.group(1))) if m else 0
    return dict(rps=rps, p50_ms=lat.get(50), p95_ms=lat.get(95), p99_ms=lat.get(99),
                responses=sum(statuses.values()), errors=errors)


def measure_startup(name, cfg, args, logdir):
    times, idle = [], []
    for i in range(args.startups):
        srv = Server(name, cfg, logdir)
        try:
            times.append(srv.wait_ready() * 1000)
            time.sleep(1.0)  # let start-up work settle before reading memory
            idle.append(rss_kib(srv.proc.pid) / 1024)
            probe_bytes = srv.probe_bytes
        finally:
            srv.stop()
        time.sleep(0.5)
        print(f"  start {i + 1}/{args.startups}: {times[-1]:.0f} ms, idle {idle[-1]:.0f} MiB", flush=True)
    return dict(
        start_ms=dict(median=statistics.median(times), min=min(times), max=max(times), runs=times),
        idle_mib=dict(median=statistics.median(idle), min=min(idle), max=max(idle)),
        probe_bytes=probe_bytes,
    )


def measure_load(name, cfg, args, logdir):
    srv = Server(name, cfg, logdir)
    result = {}
    try:
        srv.wait_ready()
        time.sleep(1.0)
        url = f"http://127.0.0.1:{srv.port}{PROBE_PATH}"
        for conns in args.concurrency:
            hey(args, url, args.warmup, conns)  # warm-up: JIT, caches, connection pools
            runs, peak = [], 0
            for r in range(args.repeats):
                sampler = PeakSampler(srv.proc.pid)
                sampler.start()
                run = hey(args, url, args.duration, conns)
                peak = max(peak, sampler.finish())
                runs.append(run)
                print(f"  c={conns} run {r + 1}/{args.repeats}: {run['rps']:.0f} req/s, "
                      f"p99 {run['p99_ms']:.1f} ms, errors {run['errors']}", flush=True)
            by_rps = sorted(runs, key=lambda x: x["rps"])
            mid = by_rps[len(by_rps) // 2]
            result[str(conns)] = dict(
                rps=mid["rps"], p50_ms=mid["p50_ms"], p95_ms=mid["p95_ms"], p99_ms=mid["p99_ms"],
                errors=sum(x["errors"] for x in runs), runs=runs, peak_rss_mib=peak / 1024,
            )
            time.sleep(1.0)
    finally:
        srv.stop()
    return result


def command_output(cmd):
    try:
        out = subprocess.run(cmd, capture_output=True, text=True, timeout=60)
    except (OSError, subprocess.TimeoutExpired):
        return "unknown"
    lines = [l for l in (out.stdout + out.stderr).splitlines() if l.strip() and "JAVA_TOOL_OPTIONS" not in l]
    return lines[-1].strip() if lines else "unknown"


def environment():
    cpu = "unknown"
    try:
        with open("/proc/cpuinfo") as f:
            for line in f:
                if line.startswith("model name"):
                    cpu = line.split(":", 1)[1].strip()
                    break
    except OSError:
        pass
    mem = None
    try:
        with open("/proc/meminfo") as f:
            mem = int(f.readline().split()[1]) // 1024
    except (OSError, ValueError):
        pass
    return dict(os=platform.platform(), cpu=cpu, cpus=os.cpu_count(), memory_mib=mem,
                python=platform.python_version(),
                java=command_output(["java", "-version"]), node=command_output(["node", "--version"]))


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--out", required=True, help="where to write the JSON results")
    ap.add_argument("--hey", required=True, help="path of the hey load generator")
    ap.add_argument("--openapi-mocker")
    ap.add_argument("--wiremock-jar")
    ap.add_argument("--prism")
    ap.add_argument("--mockoon")
    ap.add_argument("--hey-version", default="hey", help="how to name the load generator in the results")
    ap.add_argument("--startups", type=int, default=10, help="start-up repetitions per tool")
    ap.add_argument("--duration", type=int, default=10, help="seconds per load run")
    ap.add_argument("--warmup", type=int, default=5, help="seconds of warm-up before each level")
    ap.add_argument("--repeats", type=int, default=3, help="load runs per concurrency level")
    ap.add_argument("--concurrency", type=lambda s: [int(x) for x in s.split(",")], default=[1, 32])
    ap.add_argument("--only", help="comma-separated tool names to run")
    args = ap.parse_args()

    table = tools(args)
    if args.only:
        wanted = {w.strip().lower() for w in args.only.split(",")}
        table = {k: v for k, v in table.items() if k.lower() in wanted}
    if not table:
        sys.exit("no tool to run: give at least one of --openapi-mocker, --wiremock-jar, --prism, --mockoon")

    logdir = tempfile.mkdtemp(prefix="om-bench-")
    results = dict(environment=environment(), settings=dict(
        startups=args.startups, duration_s=args.duration, warmup_s=args.warmup, repeats=args.repeats,
        concurrency=args.concurrency, probe=f"GET {PROBE_PATH}", load_generator=args.hey_version,
    ), tools={})
    try:
        for name, cfg in table.items():
            print(f"== {name}", flush=True)
            entry = dict(version=command_output(cfg["version"]))
            entry.update(measure_startup(name, cfg, args, logdir))
            entry["load"] = measure_load(name, cfg, args, logdir)
            results["tools"][name] = entry
    finally:
        shutil.rmtree(logdir, ignore_errors=True)
    os.makedirs(os.path.dirname(os.path.abspath(args.out)), exist_ok=True)
    with open(args.out, "w", encoding="utf-8", newline="\n") as f:
        json.dump(results, f, indent=2)
        f.write("\n")
    print(f"wrote {args.out}")


if __name__ == "__main__":
    main()
