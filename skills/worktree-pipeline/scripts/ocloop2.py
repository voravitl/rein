"""Wait for worker_done/escalation/question; ack heartbeat-only batches every minute so they do not pile up.
Prints the deliveryId of a real batch: ack it yourself after handling (check --ack <id> --peek)."""
import json, os, subprocess, sys, time
run, total_s = sys.argv[1], int(sys.argv[2])
end = time.time() + total_s
NOISE = ("heartbeat", "status")
STATUS_LOG = os.path.join(os.environ.get("PIPELINE_LOGDIR", os.path.expanduser("~/.cache/worktree-pipeline/logs")), "orca-status.log")
os.makedirs(os.path.dirname(STATUS_LOG), exist_ok=True)
def check(*args):
    out = subprocess.run(["orca", "orchestration", "check", "--run", run, *args, "--json"], capture_output=True, text=True).stdout
    try: return json.loads(out)["result"]
    except Exception: return {"raw": out[-1500:]}
while time.time() < end:
    # The server's --types filter is not strict: a "heartbeat" batch can carry worker_done or a question.
    # Ack a batch only when it holds heartbeats alone; otherwise report it (never swallow a real message).
    hb = check("--types", "heartbeat,status")
    hb_msgs = hb.get("messages") or []
    if hb.get("deliveryId") and all(m.get("type") in NOISE for m in hb_msgs):
        for m in hb_msgs:
            if m.get("type") == "status":  # informational progress notes: keep them in a log, do not wake up
                with open(STATUS_LOG, "a") as fh: fh.write(f"{m.get('id')} {m.get('subject')}: {(m.get('body') or '')[:500]}\n")
        check("--ack", hb["deliveryId"], "--peek", "--types", "heartbeat,status")
        r = check("--wait", "--types", "worker_done,escalation,question", "--timeout-ms", "60000")
    elif hb.get("deliveryId") or "raw" in hb:
        r = hb   # a batch that holds a real message (the --types filter is not strict), or an orca error
    else:
        # nothing pending: block on the real types instead of polling again at once (no idle busy loop)
        r = check("--wait", "--types", "worker_done,escalation,question", "--timeout-ms", "60000")
    msgs = [m for m in (r.get("messages") or []) if m.get("type") not in NOISE]
    if msgs or "raw" in r:
        print("delivery", r.get("deliveryId"))
        for m in msgs:
            p = m.get("payload") or {}
            p = json.loads(p) if isinstance(p, str) else p
            print(f"--- {m['id']} {m['type']} task={p.get('taskId')} dispatch={p.get('dispatchId')} outcome={p.get('outcome')}")
            print("subject:", m.get("subject")); print((m.get("body") or "")[:3000])
            for k in ("question", "reportPath", "filesModified"):
                if p.get(k): print(k, ":", str(p.get(k))[:1500])
        if "raw" in r: print("RAW", r["raw"])
        sys.exit(0)
print("total time out")
