#!/usr/bin/env python3
"""Dispatch the existing Rein supervisor through one owned local Orca terminal.

Orca 1.4.229 CLI schemas: terminal-format.js, handlers/terminal-send.js and
runtime/terminal-prompt-mutation-recovery.js. Transport receipts are audit data;
they do not replace Rein's controller, graph journal, leases or process proof.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shlex
import secrets
import subprocess
import sys
import tempfile

CAPABILITY = "terminal.prompt-delivery.v1"
ORCA_OVERRIDES = ("ORCA_PAIRING_CODE", "ORCA_REMOTE_PAIRING", "ORCA_ENVIRONMENT", "ORCA_USER_DATA_PATH")
PATH_SETTINGS = ("REIN_PROFILE", "PIPELINE_CONTRACTS", "PIPELINE_LEDGER", "PIPELINE_LOGDIR",
                 "PIPELINE_RUNS", "REIN_RUN_DIR", "PIPELINE_FALLBACK", "PIPELINE_PRICES")


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def absolute(value, name):
    if not isinstance(value, str) or any(c in value for c in ("\n", "\r", "\0")):
        raise ValueError(f"{name} must be a path without newlines or NUL")
    p = Path(value)
    if not p.is_absolute() or str(p) != value or p.resolve() != p:
        raise ValueError(f"{name} requires an absolute canonical path")
    return p


def persist(path, data):
    fd, temporary = tempfile.mkstemp(prefix=".orca-transport-", dir=path.parent)
    try:
        with os.fdopen(fd, "w") as out:
            json.dump(data, out, indent=2)
            out.write("\n")
            out.flush()
            os.fsync(out.fileno())
        os.replace(temporary, path)
        directory = os.open(path.parent, os.O_RDONLY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


class Transport:
    def __init__(self, args):
        self.args = args
        self.receipt = absolute(args.receipt, "receipt")
        self.state = {}

    def call(self, argv, environment=None):
        # No shell, no retry. The exact mutation request is saved before invocation.
        p = subprocess.run(argv, capture_output=True, text=True, timeout=self.args.timeout,
                           **({"env": environment} if environment is not None else {}))
        if len(p.stdout) > 1 << 20 or len(p.stderr) > 1 << 20:
            raise ValueError("CLI response exceeds 1 MiB; outcome unknown, do not retry")
        if p.returncode:
            if self.state.get("pending_request"):
                self.state["failed_cli_response"] = {"exit": p.returncode, "stdout": p.stdout, "stderr": p.stderr}
                self.save()
            raise ValueError(f"CLI exit {p.returncode}; outcome unknown, inspect owned resource; {p.stderr[:2000]}")
        value = json.loads(p.stdout)
        if not isinstance(value, dict):
            raise ValueError("CLI response must be an object")
        return value

    def save(self):
        persist(self.receipt, self.state)

    def status(self):
        if any(key in os.environ for key in ORCA_OVERRIDES):
            raise ValueError("Orca remote selectors or metadata override are not supported by this local transport")
        response = self.call([self.args.orca, "status", "--json"])
        if response.get("result", {}).get("target", {}).get("kind") != "local":
            raise ValueError("Orca status does not prove a local IPC target")
        runtime = response.get("result", {}).get("runtime", {})
        identity = runtime.get("runtimeId")
        if runtime.get("reachable") is not True or not isinstance(identity, str) or not identity or response.get("_meta", {}).get("runtimeId") != identity:
            raise ValueError("reachable runtime identity missing")
        if CAPABILITY not in runtime.get("capabilities", []):
            raise ValueError("runtime lacks durable prompt delivery")
        if self.state.get("runtime_id", identity) != identity:
            raise ValueError("runtime identity changed")
        return identity

    def orca(self, tail, mutation=False):
        self.status()
        if mutation:
            self.state["pending_request"] = tail
            self.save()
        response = self.call([self.args.orca, *tail, "--json"])
        if mutation:
            self.state["last_response"] = response
            self.save()  # Preserve a possibly accepted response even if identity is wrong.
        # Orca's runtime acknowledgement is authoritative; a status check alone
        # cannot establish which runtime handled a mutation.
        if response.get("_meta", {}).get("runtimeId") != self.state["runtime_id"]:
            raise ValueError("response runtime identity missing or mismatched; no retry")
        if mutation:
            self.state.pop("pending_request", None)
            self.save()
        return response.get("result", {})

    def verify_terminal(self):
        handle, repo = self.state["terminal"], self.state["repo"]
        inventory = self.orca(["terminal", "list", "--worktree", "path:" + repo, "--limit", "100"])
        if inventory.get("truncated") is not False:
            raise ValueError("terminal inventory is truncated or completeness unknown")
        rows = [r for r in inventory.get("terminals", []) if r.get("handle") == handle]
        if len(rows) != 1:
            raise ValueError("exact owned terminal absent or duplicated")
        row = rows[0]
        if row.get("executionHostId") != "local" or row.get("worktreePath") != repo or row.get("connected") is not True:
            raise ValueError("terminal host, worktree or connection mismatch")
        terminal = self.orca(["terminal", "show", "--terminal", handle]).get("terminal", {})
        if terminal.get("handle") != handle or terminal.get("worktreePath") != repo or terminal.get("connected") is not True or terminal.get("writable") is not True or not terminal.get("ptyId"):
            raise ValueError("terminal show identity or writable PTY proof missing")
        incarnation = terminal.get("incarnationId")
        if terminal.get("executionHostId") != "local" or not isinstance(incarnation, str) or not incarnation:
            raise ValueError("terminal show local host or process incarnation missing")
        if self.state.get("pty_id", terminal["ptyId"]) != terminal["ptyId"]:
            raise ValueError("terminal PTY incarnation changed")
        if self.state.get("process_incarnation", incarnation) != incarnation:
            raise ValueError("terminal process incarnation changed")
        self.state["pty_id"] = terminal["ptyId"]
        self.state["process_incarnation"] = incarnation
        self.save()

    def dispatch(self):
        if os.name != "posix":
            raise ValueError("local Orca shell transport currently requires POSIX; no terminal created")
        if any(key in os.environ for key in ORCA_OVERRIDES):
            raise ValueError("Orca remote selectors or metadata override are not supported by this local transport")
        rein = absolute(self.args.rein, "rein")
        plan = absolute(self.args.plan, "plan")
        absolute(self.args.orca, "orca")
        if not rein.is_file() or not os.access(rein, os.X_OK):
            raise ValueError("rein is not an executable file")
        raw = plan.read_bytes()
        if len(raw) > 1 << 20:
            raise ValueError("plan exceeds 1 MiB")
        parsed = json.loads(raw)
        repo = absolute(parsed.get("repo", ""), "plan repo")
        if parsed.get("schema") not in (2, 3):
            raise ValueError("local graph requires a schema-2 or schema-3 plan")
        if self.receipt.is_relative_to(repo):
            raise ValueError("transport receipt must be outside the contracted checkout")
        settings = {key: os.environ.get(key) for key in PATH_SETTINGS}
        if not settings["REIN_PROFILE"]:
            raise ValueError("explicit existing canonical REIN_PROFILE is required")
        for key, value in settings.items():
            if value is not None:
                absolute(value, key)
        profile = Path(settings["REIN_PROFILE"])
        if not profile.is_file():
            raise ValueError("REIN_PROFILE must be an existing file")
        profile_raw = profile.read_bytes()
        process_runner = Path(__file__).resolve().with_name("supervisor-process.py")
        process_runner_raw = process_runner.read_bytes()
        python = Path(sys.executable).resolve()
        process_receipt = self.receipt.with_name(self.receipt.name + ".process.json")
        if process_receipt.exists():
            raise ValueError("process receipt already exists; do not redispatch")
        validator_environment = dict(os.environ)
        invocation_id = secrets.token_hex(32)
        environment_argv = ["/usr/bin/env"]
        for key, value in settings.items():
            if value is None:
                validator_environment.pop(key, None)
                environment_argv.extend(["-u", key])
        for key, value in settings.items():
            if value is not None:
                validator_environment[key] = value
                environment_argv.append(key + "=" + value)
        # Correlates telemetry only; never grants execution or cancellation authority.
        environment_argv.append("REIN_SUPERVISOR_INVOCATION=" + invocation_id)
        # This is the existing authoritative read-only validator, before Orca creation.
        checked = self.call([str(rein), "supervisor", "validate", "--plan", str(plan)], validator_environment)
        if plan.read_bytes() != raw or profile.read_bytes() != profile_raw:
            raise ValueError("plan or owner profile changed during validation")
        if checked.get("valid") is not True or checked.get("plan_hash") != digest(raw) or checked.get("run") != parsed.get("run"):
            raise ValueError("Rein did not validate the exact plan bytes and run")
        self.receipt.parent.mkdir(parents=True, exist_ok=True)
        fd = os.open(self.receipt, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        os.close(fd)  # Existing receipt always refuses redispatch, including ambiguous outcomes.
        self.state = {"schema": 1, "phase": "validated", "repo": str(repo), "run": parsed["run"],
                      "plan": str(plan), "plan_hash": digest(raw), "rein": str(rein), "orca": self.args.orca,
                      "path_settings": settings, "profile_hash": digest(profile_raw),
                      "invocation_id": invocation_id,
                      "process_receipt": str(process_receipt), "process_runner_hash": digest(process_runner_raw),
                      "command": "exec " + shlex.join(environment_argv + [str(python), str(process_runner),
                          "--rein", str(rein), "--plan", str(plan), "--receipt", str(process_receipt),
                          "--invocation", invocation_id, "--plan-hash", digest(raw), "--profile-hash", digest(profile_raw)])}
        self.save()
        self.state["runtime_id"] = self.status()
        self.save()
        registered = self.orca(["worktree", "show", "--worktree", "path:" + str(repo)]).get("worktree", {})
        if (registered.get("path") != str(repo) or registered.get("hostId") != "local"
                or registered.get("identity", {}).get("executionHostId") != "local"):
            raise ValueError("Orca did not prove the exact registered local worktree; no terminal created")
        self.state["registered_worktree"] = {key: registered.get(key) for key in ("id", "path", "hostId", "identity")}
        self.save()
        created = self.orca(["terminal", "create", "--worktree", "path:" + str(repo), "--title", "Crux Rein supervisor"], mutation=True)
        handle = created.get("terminal", {}).get("handle")
        if not isinstance(handle, str) or not handle:
            raise ValueError("create returned no exact terminal handle; do not retry")
        self.state["terminal"], self.state["phase"] = handle, "created_empty"
        self.state["create_receipt"] = created
        self.state["create_response"] = self.state["last_response"]
        self.save()
        self.verify_terminal()
        if plan.read_bytes() != raw or profile.read_bytes() != profile_raw or process_runner.read_bytes() != process_runner_raw:
            raise ValueError("plan or owner profile changed before dispatch, or process runner changed")
        self.state["phase"] = "send_pending"
        self.save()
        sent = self.orca(["terminal", "send", "--terminal", handle, "--text", self.state["command"], "--enter"], mutation=True)
        receipt = sent.get("send", {})
        prompt = receipt.get("prompt", {})
        self.state["send_receipt"] = receipt
        self.save()  # Preserve even a refused/malformed acknowledgement; never resend.
        if prompt.get("processIncarnation") != self.state["process_incarnation"]:
            raise ValueError("prompt receipt process incarnation changed; delivery uncertain, do not retry")
        if receipt.get("handle") != handle or receipt.get("accepted") is not True or not isinstance(prompt.get("requestId"), str) or prompt.get("requestId") in ("", "unsupported-old-host") or (not isinstance(prompt.get("processIncarnation"), str) or prompt.get("processIncarnation") in ("", "unknown")) or type(prompt.get("generation")) is not int or prompt.get("observation") == "unsupported" or "input_accepted" not in prompt.get("stages", []):
            raise ValueError("durable exact prompt receipt missing; delivery unknown, do not retry")
        self.state["phase"] = "dispatched"
        self.save()
        return {"status": "dispatched", "terminal": handle, "request_id": prompt["requestId"],
                "receipt": str(self.receipt), "supervisor_completion": "not_verified"}

    def cancel(self):
        if any(key in os.environ for key in ORCA_OVERRIDES):
            raise ValueError("Orca remote selectors or metadata override are not supported by this local transport")
        self.state = json.loads(self.receipt.read_text())
        if self.state.get("phase") not in ("dispatched", "send_pending") or not self.state.get("terminal"):
            raise ValueError("receipt does not prove an exact dispatched terminal; no interrupt")
        if self.args.orca != self.state.get("orca"):
            raise ValueError("Orca executable changed")
        self.verify_terminal()
        # Show exposes incarnationId but not current prompt generation, so it cannot
        # bind both fields of the accepted prompt. The PTY may contain a new job.
        # Interrupting it could target unrelated work, so retain the terminal.
        self.state["phase"] = "cancellation_blocked_unverified_process"
        self.save()
        raise ValueError("current supervisor process incarnation and generation cannot be observed; no interrupt or close sent; Rein graph and process termination remain unverified")

    def observe(self):
        # Read-only correlation with Rein's validated journal; no terminal actions.
        state = json.loads(self.receipt.read_text())
        rein = absolute(self.args.rein, "rein")
        if str(rein) != state.get("rein"):
            raise ValueError("Rein executable does not match the recorded invocation")
        plan = absolute(state.get("plan"), "recorded plan")
        if digest(plan.read_bytes()) != state.get("plan_hash"):
            raise ValueError("recorded plan changed; invocation cannot be correlated")
        invocation = state.get("invocation_id", "")
        if len(invocation) != 64 or any(c not in "0123456789abcdef" for c in invocation):
            raise ValueError("receipt lacks a valid invocation ID; do not resend")
        settings = state.get("path_settings", {})
        if set(settings) != set(PATH_SETTINGS) or not settings.get("REIN_PROFILE"):
            raise ValueError("recorded path settings missing")
        environment = dict(os.environ)
        for key, value in settings.items():
            if value is None:
                environment.pop(key, None)
            else:
                absolute(value, key)
                environment[key] = value
        if digest(Path(settings["REIN_PROFILE"]).read_bytes()) != state.get("profile_hash"):
            raise ValueError("recorded owner profile changed")
        observed = self.call([str(rein), "supervisor", "invocation", "--plan", str(plan),
                              "--invocation", invocation], environment)
        start = observed.get("start", {})
        expected = {"invocation_id": invocation, "run": state.get("run"),
                    "plan_hash": state["plan_hash"], "repo": state.get("repo")}
        if any(start.get(key) != value for key, value in expected.items()):
            raise ValueError("supervisor invocation identity differs from transport receipt")
        returned = observed.get("return")
        if returned is not None and (not isinstance(returned, dict) or any(returned.get(key) != value for key, value in expected.items())
                                     or returned.get("outcome") != "graph_returned"):
            raise ValueError("supervisor return identity or outcome differs")
        process_exit = None
        process_path = state.get("process_receipt")
        if process_path is not None:
            process_path = absolute(process_path, "recorded process receipt")
            if process_path != self.receipt.with_name(self.receipt.name + ".process.json"):
                raise ValueError("process receipt path differs from owned receipt")
            if process_path.exists():
                raw_process = process_path.read_bytes()
                if len(raw_process) > 1 << 20:
                    raise ValueError("process receipt exceeds 1 MiB")
                process = json.loads(raw_process)
                identity = dict(expected, rein=str(rein), plan=str(plan), profile_hash=state["profile_hash"])
                if (type(process.get("schema")) is not int or process["schema"] != 1
                        or type(process.get("parent_pid")) is not int or process["parent_pid"] <= 0
                        or type(process.get("child_pid")) is not int or process["child_pid"] <= 0
                        or process.get("phase") not in ("spawn_pending", "running", "exited")
                        or process.get("argv") != [str(rein), "supervisor", "run", "--plan", str(plan)]
                        or any(process.get(key) != value for key, value in identity.items())
                        or process.get("child_pid") != start.get("pid")):
                    raise ValueError("parent wait receipt does not match exact supervisor invocation")
                if process.get("phase") == "exited":
                    if type(process.get("process_exit_code")) is not int:
                        raise ValueError("parent wait receipt lacks actual process exit code")
                    process_exit = process["process_exit_code"]
        return {"status": "graph_returned" if returned is not None else "graph_started",
                "invocation": observed, "receipt": str(self.receipt),
                "process_termination": "not_verified", "supervisor_process_exit_code": process_exit,
                "supervisor_process_termination": "verified_by_parent_wait" if process_exit is not None else "not_verified",
                "retry": False}


def render_result(result, output_format):
    if output_format == "json":
        return json.dumps(result, ensure_ascii=False)
    if result["status"] == "preview":
        return ("สถานะ: แสดงแผน ยังไม่ได้ส่งงาน\n"
                "การทำงาน: 0 ครั้ง | การเรียกโมเดล: 0 ครั้ง\n"
                "ส่งงานจริงด้วย --execute พร้อม --rein --plan --orca --receipt ที่เป็น absolute path\n"
                "การยกเลิกยังไม่พร้อม: ต้องมีหลักฐานระบุ process ปัจจุบันก่อนสั่งหยุด")
    if result["status"] == "dispatched":
        return ("สถานะ: Orca รับคำสั่งแล้ว\n"
                f"Terminal: {result['terminal']}\n"
                f"หลักฐานการส่ง: {result['receipt']}\n"
                "ยังไม่ยืนยัน: Rein ทำงานสำเร็จ การตรวจผ่าน หรือ process หยุดแล้ว\n"
                "ขั้นต่อไป: ตรวจผลจาก Rein supervisor และหลักฐาน process ก่อนสรุปว่างานจบ")
    if result["status"] in ("graph_started", "graph_returned"):
        returned = result["invocation"].get("return")
        lines = ["สถานะ: Rein supervisor คืนผลแล้ว" if returned is not None else "สถานะ: Rein supervisor เริ่มทำงานแล้ว",
                 f"หลักฐานของรอบนี้: {result['receipt']}"]
        if returned is not None:
            graph = returned.get("result") or {}
            state = graph.get("state", "unknown")
            labels = {"WAITING_HUMAN": "รอผู้รับผิดชอบตรวจผล", "WAITING_QUOTA": "รอโควตา",
                      "WAITING_AUTH": "รอยืนยันสิทธิ์บัญชี", "WAITING_BUDGET": "รอวงเงิน",
                      "WAITING_REVIEW": "รอผู้ตรวจ", "BLOCKED_RECONCILIATION": "หยุดเพื่อกระทบยอดสถานะก่อนทำต่อ"}
            lines.append("ผลจาก Rein: " + labels.get(state, "ยังแปลสถานะนี้ไม่ได้") + " (" + state + ")")
            lines.append("งานที่ graph บันทึกว่าผ่าน: " + str(len(graph.get("completed") or [])))
            if graph.get("reason"):
                lines.append("รายละเอียดจาก Rein: " + graph["reason"])
            if returned.get("error"):
                if returned["error"].startswith("model calls disabled by config:"):
                    lines.append("คำอธิบาย: config ปิดการเรียกโมเดลไว้ จึงยังไม่ได้เริ่ม worker")
                lines.append("เหตุผลจากระบบ: " + returned["error"])
        if result.get("supervisor_process_termination") == "verified_by_parent_wait":
            lines.append("OS ยืนยันว่า supervisor จบแล้ว โดย process แม่รอรับผลโดยตรง (exit " + str(result["supervisor_process_exit_code"]) + ")")
            lines.append("ยังไม่ยืนยันงานลูกทั้งหมด ห้ามใช้ receipt นี้สั่งหยุดหรือส่งงานซ้ำ")
        else:
            lines.append("ยังไม่ยืนยันว่า process และงานลูกทั้งหมดหยุดแล้ว ห้ามใช้ผลนี้สั่งหยุดหรือส่งงานซ้ำ")
        return "\n".join(lines)
    return ("สถานะ: หยุดขั้นตอนนี้แล้ว\n"
            f"เหตุผลจากระบบ: {result['error']}\n"
            "ไม่ส่งซ้ำอัตโนมัติ เพราะคำสั่งเดิมอาจถูกรับไปแล้ว\n"
            "ขั้นต่อไป: ตรวจ receipt และสถานะทรัพยากรเดิมก่อนดำเนินการต่อ")


def main(argv=None):
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--execute", action="store_true")
    p.add_argument("--cancel", action="store_true")
    p.add_argument("--observe", action="store_true", help="read the exact Rein invocation without sending or cancelling")
    p.add_argument("--rein")
    p.add_argument("--plan")
    p.add_argument("--orca")
    p.add_argument("--receipt")
    p.add_argument("--timeout", type=int, default=10)
    p.add_argument("--format", choices=("json", "text"), default="json",
                   help="text displays a concise Thai status; JSON remains machine-readable")
    args = p.parse_args(argv)
    if not 1 <= args.timeout <= 60:
        p.error("timeout must be 1..60 seconds")
    if args.observe and (args.execute or args.cancel):
        p.error("observe cannot execute or cancel")
    if not args.observe and not args.execute:
        print(render_result({"status": "preview", "actions": 0, "model_calls": 0,
                             "next": "--execute requires absolute --rein --plan --orca --receipt; --cancel reports missing process proof without interrupting"}, args.format))
        return 0
    if args.observe:
        if not args.rein or not args.receipt:
            p.error("observe requires absolute --rein --receipt")
    elif not args.orca or not args.receipt or (not args.cancel and (not args.rein or not args.plan)):
        p.error("execute requires absolute --rein --plan --orca --receipt (cancel requires Orca and receipt)")
    transport = None
    try:
        if not args.observe:
            absolute(args.orca, "orca")
        transport = Transport(args)
        result = transport.observe() if args.observe else (transport.cancel() if args.cancel else transport.dispatch())
        print(render_result(result, args.format))
        return 0
    except (ValueError, OSError, subprocess.SubprocessError, KeyError, TypeError) as error:
        if not args.observe and transport and transport.state:
            transport.state["error"] = str(error)
            transport.save()
        print(render_result({"status": "blocked", "error": str(error), "retry": False}, args.format), file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
