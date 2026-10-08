#!/usr/bin/env python3
"""Free Orca resources of one Run: release settled workers, stop finished-but-still-dispatched ones,
close their terminals. Never touches a dispatch that is still working unless you list it in --stop.

usage: orca_cleanup.py <run_id> [--stop ctx_a,ctx_b] [--dry-run]
  --stop   dispatches whose work is done but that never settled (lost worker_done, Orca outage)
"""
import argparse, json, subprocess

def orca(*args):
    out = subprocess.run(["orca", *args, "--json"], capture_output=True, text=True).stdout
    try: return json.loads(out)
    except Exception: return {"ok": False, "error": {"message": out[-300:]}}

ap = argparse.ArgumentParser(); ap.add_argument("run"); ap.add_argument("--stop", default=""); ap.add_argument("--dry-run", action="store_true")
a = ap.parse_args(); stop = {s for s in a.stop.split(",") if s}
res = orca("orchestration", "worker-list", "--run", a.run)
if not res.get("ok") or "workers" not in (res.get("result") or {}):
    raise SystemExit(f"[orca_cleanup] worker-list failed: {(res.get('error') or {}).get('message', res)}")
workers = res["result"]["workers"]
failed = []; stopped_ok = set()
terms = set()
for w in workers:
    d, ds, ts = w["dispatchId"], w.get("dispatchStatus"), w.get("terminalState")
    if ts == "closed": continue
    if d in stop:
        print("stop   ", d, ds)
        if a.dry_run: terms.add(w.get("agentTerminalHandle"))
        else:
            r = orca("orchestration", "worker-stop", "--dispatch", d); print("  ->", r.get("ok"))
            if r.get("ok"): terms.add(w.get("agentTerminalHandle")); stopped_ok.add(d)   # never close the terminal of a worker that did not stop
            else: failed.append(f"stop {d}")
    elif ds in ("completed", "failed"):
        print("release", d, ds); terms.add(w.get("agentTerminalHandle"))
        if not a.dry_run and (w.get("resource") or {}).get("releaseState") not in ("released",):
            r = orca("orchestration", "worker-release", "--dispatch", d); print("  ->", r.get("ok"))
            if not r.get("ok"): failed.append(f"release {d}")
    else:
        print("keep   ", d, ds, "(still working; pass --stop to end it)")
# A terminal stays open while any of its dispatches is still working, including one whose stop failed (terminals are reused).
ended = stopped_ok if not a.dry_run else stop
active = {w.get("agentTerminalHandle") for w in workers if w.get("dispatchStatus") not in ("completed", "failed") and w["dispatchId"] not in ended and w.get("terminalState") != "closed"}
for t in sorted(t for t in terms - active if t):
    r = {"ok": "dry-run"} if a.dry_run else orca("terminal", "close", "--terminal", t)
    code = (r.get("error") or {}).get("code", "")
    print("close  ", t, r.get("ok"), code)
    if not r.get("ok") and code != "terminal_handle_stale": failed.append(f"close {t}")
if failed: raise SystemExit(f"[orca_cleanup] FAILED: {', '.join(failed)}")
