#!/usr/bin/env python3
"""Inspect installed Crux metadata through Codex; never submit a model turn."""
import argparse
import asyncio
import json
import os
from pathlib import Path
import shutil
import signal
import sys

PLUGIN_ID = "crux@rein"
EVENTS = ("sessionStart", "userPromptSubmit")
REQUIRED_SKILLS = ("crux:crux-mode", "crux:thai-communication")
LIMIT = 8 * 1024 * 1024


class InspectionError(Exception):
    pass


async def exchange(reader, writer, cwd):
    async def send(message):
        writer.write((json.dumps(message) + "\n").encode())
        await writer.drain()

    async def request(request_id, method, params):
        await send({"id": request_id, "method": method, "params": params})
        # Bound notifications as well as individual messages. Do not print raw
        # SDK responses: other installed plugins can contain private metadata.
        total = 0
        for _ in range(512):
            line = await reader.readline()
            total += len(line)
            if not line or total > LIMIT:
                raise InspectionError("protocol_limit_or_eof")
            message = json.loads(line)
            if not isinstance(message, dict):
                raise InspectionError("invalid_response")
            if message.get("id") == request_id:
                if "error" in message or not isinstance(message.get("result"), dict):
                    raise InspectionError("rpc_error")
                return message["result"]
            if "method" in message and "id" in message:
                # No permission, user-input or other server request is approved.
                raise InspectionError("unexpected_server_request")
        raise InspectionError("protocol_limit")

    await request(1, "initialize", {"clientInfo": {"name": "crux-readiness", "version": "0.1.0"}})
    await send({"method": "initialized", "params": {}})
    hooks = await request(2, "hooks/list", {"cwds": [cwd]})
    skills = await request(3, "skills/list", {"cwds": [cwd], "forceReload": True})
    return hooks, skills


def summarize(hooks_result, skills_result, cwd):
    def entry(result, key):
        data = result.get("data")
        if not isinstance(data, list):
            raise InspectionError("invalid_schema")
        selected = [row for row in data if isinstance(row, dict) and row.get("cwd") == cwd]
        if len(selected) != 1:
            raise InspectionError("workspace_not_unique")
        row = selected[0]
        if not isinstance(row.get(key), list) or not isinstance(row.get("errors"), list):
            raise InspectionError("invalid_schema")
        if row["errors"]:
            raise InspectionError("discovery_errors")
        if key == "hooks":
            if not isinstance(row.get("warnings"), list):
                raise InspectionError("invalid_schema")
            if row["warnings"]:
                raise InspectionError("discovery_warnings")
        return row[key]

    hooks = entry(hooks_result, "hooks")
    skills = entry(skills_result, "skills")
    owned_hooks = [row for row in hooks if isinstance(row, dict) and row.get("pluginId") == PLUGIN_ID]
    owned_skills = [row for row in skills if isinstance(row, dict) and row.get("pluginId") == PLUGIN_ID]
    # Require native plugin ownership; a same-named repository skill is weaker evidence.
    if any(not isinstance(row.get("name"), str) for row in owned_skills):
        raise InspectionError("invalid_schema")
    available = {row["name"] for row in owned_skills if row.get("enabled") is True}
    checks = []
    for event in EVENTS:
        rows = [row for row in owned_hooks if row.get("eventName") == event]
        ready = bool(rows) and all(row.get("enabled") is True and row.get("trustStatus") in
                                   ("trusted", "managed") for row in rows)
        checks.append({"event": event, "count": len(rows), "status": "READY" if ready else "NOT_READY",
                       "trust": sorted({row.get("trustStatus") if row.get("trustStatus") in
                                       ("trusted", "managed", "untrusted", "modified") else "unknown" for row in rows})})
    missing = [name for name in REQUIRED_SKILLS if name not in available]
    ready = not missing and all(row["status"] == "READY" for row in checks)
    return {"schema": 1, "scope": "codex_installed_plugin_metadata_only", "plugin_id": PLUGIN_ID,
            "status": "READY" if ready else "NOT_READY", "enabled_skills": len(available),
            "missing_required_skills": missing, "hooks": checks,
            "live_acceptance": "NOT_VERIFIED", "model_requests": 0, "trust_modified": False}


async def inspect(cwd, timeout):
    executable = shutil.which("codex")
    if not executable:
        raise InspectionError("codex_not_found")
    process = await asyncio.create_subprocess_exec(
        executable, "app-server", cwd=cwd, stdin=asyncio.subprocess.PIPE,
        stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.DEVNULL,
        limit=LIMIT, start_new_session=True)
    try:
        hooks, skills = await asyncio.wait_for(exchange(process.stdout, process.stdin, cwd), timeout)
        return summarize(hooks, skills, cwd)
    finally:
        process.stdin.close()
        try:
            await asyncio.wait_for(process.wait(), 2)
        except asyncio.TimeoutError:
            # Signal only this freshly created process group, never stored PIDs.
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            await process.wait()


def render(report):
    if report["status"] == "ERROR":
        return "ตรวจ Codex ไม่สำเร็จ (" + report["error"] + ")\nยังไม่ยืนยันการติดตั้งหรือการทำงานจริง"
    labels = {"sessionStart": "เริ่ม session", "userPromptSubmit": "รับคำสั่งผู้ใช้"}
    lines = ["Codex: " + ("พบ Crux และ hooks พร้อมตาม metadata" if report["status"] == "READY"
                              else "Crux ยังไม่พร้อมตาม metadata"),
             f"พบ skills ที่เปิดใช้และเป็นของ Crux: {report['enabled_skills']}"]
    for row in report["hooks"]:
        state = "พร้อม" if row["status"] == "READY" else "ยังไม่พร้อม"
        lines.append(f"Hook {labels[row['event']]}: {state} (trust={','.join(row['trust']) or 'ไม่พบ'})")
    if report["missing_required_skills"]:
        lines.append("ไม่พบ skill ที่จำเป็น: " + ", ".join(report["missing_required_skills"]))
    lines.extend(["ผลนี้ยังไม่ยืนยันการตอบภาษาไทย การแบ่งงาน หรือการส่งงานข้าม provider",
                  "ตรวจเฉพาะ metadata ไม่ส่ง prompt และไม่แก้ trust"])
    return "\n".join(lines)


def main(argv=None):
    parser = argparse.ArgumentParser(description="ตรวจ Crux ที่ Codex โหลดจริง โดยไม่เรียกโมเดลหรือแก้ trust")
    parser.add_argument("--cwd", type=Path, default=Path.cwd())
    parser.add_argument("--timeout", type=int, default=15)
    parser.add_argument("--format", choices=("text", "json"), default="text")
    args = parser.parse_args(argv)
    if not args.cwd.is_dir() or not 1 <= args.timeout <= 60:
        parser.error("ต้องใช้ directory ที่มีอยู่ และ timeout 1–60 วินาที")
    try:
        report = asyncio.run(inspect(str(args.cwd.resolve()), args.timeout))
    except (InspectionError, OSError, ValueError, asyncio.TimeoutError) as error:
        # SDK error strings/JSON/stderr may carry private information.
        report = {"schema": 1, "status": "ERROR", "error":
                  str(error) if isinstance(error, InspectionError) else type(error).__name__,
                  "live_acceptance": "NOT_VERIFIED", "model_requests": 0, "trust_modified": False}
    print(json.dumps(report, ensure_ascii=False, indent=2) if args.format == "json" else render(report))
    return {"READY": 0, "NOT_READY": 2, "ERROR": 1}[report["status"]]


if __name__ == "__main__":
    sys.exit(main())
