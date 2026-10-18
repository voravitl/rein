#!/usr/bin/env python3
"""Run offline adapter suites and optional native metadata checks; no model turns."""
import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys

PLUGIN = Path(__file__).resolve().parents[1]
# Keep shared package validation separate from each harness adapter's evidence.
SUITES = (
    ("codex", ("adapters/codex/hooks/session-mode.test.mjs", "tests/agents.test.mjs")),
    ("claude", ("tests/claude.test.mjs",)),
    ("antigravity", ("tests/antigravity.test.mjs",)),
    ("opencode2", ("tests/opencode2.test.mjs",)),
    ("shared_package", ("tests/package.test.mjs",)),
)
LIVE_CHECKS = ("native_runtime", "semantic_thai", "guard", "delegate", "stop", "handoff")
LABELS = {"codex": "Codex", "claude": "Claude Code", "antigravity": "Antigravity",
          "opencode2": "OpenCode2", "shared_package": "แพ็กเกจร่วม"}
OUTPUT_LIMIT = 131072


def _output(value):
    if isinstance(value, bytes):
        value = value.decode("utf-8", errors="replace")
    return (value or "")[:OUTPUT_LIMIT]


def _run_suite(command, timeout):
    # Provider credentials, model opt-ins and NODE_OPTIONS/NODE_PATH do not enter
    # these processes. Existing tests use temporary directories and local Node.
    environment = {key: os.environ[key] for key in
                   ("PATH", "TMPDIR", "TEMP", "TMP", "LANG", "LC_ALL", "SystemRoot")
                   if key in os.environ}
    return subprocess.run(command, cwd=PLUGIN, env=environment, stdin=subprocess.DEVNULL,
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                          encoding="utf-8", errors="replace", timeout=timeout, check=False)


def collect_report(timeout=60, *, _runner=None, _node=None):
    """Dependency injection is internal to unit tests; CLI accepts no commands."""
    runner = _runner or _run_suite
    node = _node or shutil.which("node")
    results = []
    for name, paths in SUITES:
        command = [node or "node", "--test", *paths]
        row = {"suite": name, "command": command, "timeout_seconds": timeout,
               "status": "ERROR", "exit_code": None, "stdout": "", "stderr": ""}
        try:
            if not node:
                raise FileNotFoundError("ไม่พบ Node.js ใน PATH")
            completed = runner(command, timeout)
            row.update(status="PASS" if completed.returncode == 0 else "FAIL",
                       exit_code=completed.returncode, stdout=_output(completed.stdout),
                       stderr=_output(completed.stderr))
            raw_out, raw_err = completed.stdout, completed.stderr
        except subprocess.TimeoutExpired as error:
            row.update(status="TIMEOUT", stdout=_output(error.stdout), stderr=_output(error.stderr))
            raw_out, raw_err = error.stdout, error.stderr
        except OSError as error:
            row["stderr"] = str(error)
            raw_out, raw_err = "", row["stderr"]
        row["output_truncated"] = any(len(value or "") > OUTPUT_LIMIT for value in (raw_out, raw_err))
        results.append(row)
    local_pass = all(row["status"] == "PASS" for row in results)
    by_name = {row["suite"]: row for row in results}
    return {
        "schema": 1, "scope": "offline_adapter_checks_only",
        "local_status": "PASS" if local_pass else "FAIL",
        "acceptance_status": "NOT_VERIFIED", "live_status": "NOT_VERIFIED",
        "native_dispatch_enabled": False,
        "harnesses": [{"harness": name, "label": LABELS[name],
                       "local_adapter_status": by_name[name]["status"],
                       "live_checks": {check: "NOT_VERIFIED" for check in LIVE_CHECKS}}
                      for name, _ in SUITES if name != "shared_package"],
        "shared_package_status": by_name["shared_package"]["status"],
        "suites": results,
    }


def render_text(report, details=False):
    summary = ("การตรวจ adapter ในเครื่องผ่านครบ" if report["local_status"] == "PASS"
               else "การตรวจ adapter ในเครื่องยังไม่ผ่านครบ")
    native_all = report.get("native_harness_metadata")
    lines = [summary + " — ยังไม่ยืนยันการทำงานจริงครบทุก harness", ""]
    if native_all:
        lines.extend(["| ระบบ | ตรวจในเครื่อง | ตรวจการโหลดจริง | งานครบวงจร |",
                      "| --- | --- | --- | --- |"])
        for row in report["harnesses"]:
            native = native_all[row["harness"]]
            if native["status"] in ("READY", "METADATA_READY"):
                state = "โหลดหรือเลือกได้"
            elif native["status"] == "NOT_READY":
                state = "ยังไม่พร้อม"
            else:
                state = "ตรวจไม่สำเร็จ"
            lines.append(f"| {row['label']} | {row['local_adapter_status']} | {state} | ยังไม่ยืนยัน |")
        lines.append(f"| แพ็กเกจร่วม | {report['shared_package_status']} | — | — |")
    else:
        lines.extend(["| ระบบ | ตรวจในเครื่อง | ตรวจระบบจริง |", "| --- | --- | --- |"])
        for row in report["harnesses"]:
            lines.append(f"| {row['label']} | {row['local_adapter_status']} | ยังไม่ยืนยัน |")
        lines.append(f"| แพ็กเกจร่วม | {report['shared_package_status']} | — |")
    lines.extend(["",
                  "ยังไม่ยืนยัน: การทำงานจริงของ harness, คุณภาพภาษาไทย, การปฏิเสธนอกขอบเขต, การแบ่งงาน, การหยุดงาน และการส่งต่องาน",
                  ("ชุดทดสอบ adapter เรียกเฉพาะ Node ในเครื่อง; เพิ่มการอ่าน Codex metadata ตาม --native-codex"
                   if "native_codex_metadata" in report else
                   "เพิ่มการตรวจ runtime ทั้งสี่แบบโดยไม่ส่ง prompt ให้โมเดล" if native_all else
                   "คำสั่งนี้เรียกเฉพาะชุดทดสอบ Node ในเครื่อง ไม่เรียก CLI ของ provider หรือโมเดล"), ""])
    if native_all:
        lines.extend(["Codex ตรวจ plugin ที่ติดตั้ง; อีกสามระบบโหลด source ในพื้นที่ทดสอบชั่วคราว",
                      "ผลโหลด source ไม่ยืนยันว่าติดตั้งเป็นค่าเริ่มต้นของผู้ใช้แล้ว",
                      "Claude/Antigravity ยืนยันชื่อจาก runtime; ยังไม่ยืนยัน revision ของ source ที่โหลด", ""])
    codex = report.get("native_codex_metadata") or (native_all or {}).get("codex")
    if codex:
        native = codex
        label = {"READY": "skills/hooks พร้อมตาม metadata",
                 "METADATA_READY": "skills/hooks พร้อมตาม metadata",
                 "NOT_READY": "skills/hooks ยังไม่พร้อม", "ERROR": "ตรวจไม่สำเร็จ"}[native["status"]]
        lines.append("Codex ที่ติดตั้งจริง: " + label)
        for hook in native.get("hooks", []):
            lines.append(f"Hook {hook['event']}: {','.join(hook['trust']) or 'ไม่พบ'}")
        if native.get("missing_required_skills"):
            lines.append("ไม่พบ skills หลัก: " + ", ".join(native["missing_required_skills"]))
        lines.extend(["ส่วนนี้อ่าน metadata จาก Codex ไม่ส่ง prompt ไม่แก้ trust และยังไม่ยืนยัน workflow จริง", ""])
    if native_all and native_all.get("antigravity", {}).get("normal_cli_initialization_side_effects_possible"):
        lines.append("Antigravity ใช้ profile ปกติ; CLI อาจเก็บข้อมูลเริ่มต้นตามปกติ โดยตัวตรวจไม่แก้ config/auth/trust")
    if native_all:
        for name, native in native_all.items():
            if native["status"] == "ERROR":
                lines.append(f"{LABELS[name]}: ตรวจไม่สำเร็จ ({native.get('error', 'unknown')})")
    for suite in report["suites"]:
        if not details and suite["status"] == "PASS":
            continue
        lines.extend([f"{LABELS[suite['suite']]}: {suite['status']} (exit={suite['exit_code']})",
                      "argv: " + json.dumps(suite["command"], ensure_ascii=False),
                      "stdout:", suite["stdout"], "stderr:", suite["stderr"]])
        if suite["output_truncated"]:
            lines.append("ตัด output ที่เกินขอบเขตของรายงาน")
    return "\n".join(lines)


def collect_native_codex():
    """Fixed metadata-only helper; no configured command or model prompt."""
    try:
        result = subprocess.run(
            [sys.executable, "-B", str(PLUGIN / "scripts/codex-readiness.py"),
             "--cwd", str(Path.cwd().resolve()), "--format", "json"],
            stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
            encoding="utf-8", errors="replace", timeout=20, check=False)
        if len(result.stdout) > OUTPUT_LIMIT:
            raise ValueError("oversized report")
        report = json.loads(result.stdout)
        if (not isinstance(report, dict) or report.get("plugin_id") != "crux@rein"
                or report.get("status") not in ("READY", "NOT_READY")
                or report.get("model_requests") != 0 or report.get("trust_modified") is not False
                or report.get("live_acceptance") != "NOT_VERIFIED"
                or result.returncode != (0 if report["status"] == "READY" else 2)):
            raise ValueError("invalid metadata report")
        return report
    except (OSError, ValueError, subprocess.TimeoutExpired):
        return {"status": "ERROR", "live_acceptance": "NOT_VERIFIED"}


def collect_native_harness(harness):
    """Pinned source-selection diagnostics; no configured provider commands."""
    if harness == "codex":
        return collect_native_codex()
    commands = {
        "claude": ("sdk-readiness.py", ["--harness", "claude", "--timeout", "30"]),
        "antigravity": ("sdk-readiness.py", ["--harness", "antigravity", "--timeout", "30"]),
        "opencode2": ("opencode-readiness.py", []),
    }
    if harness not in commands:
        raise ValueError("unsupported harness")
    script, arguments = commands[harness]
    try:
        result = subprocess.run(
            [sys.executable, "-B", str(PLUGIN / "scripts" / script), *arguments, "--format", "json"],
            stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
            encoding="utf-8", errors="replace", timeout=80, check=False)
        if len(result.stdout) > OUTPUT_LIMIT:
            raise ValueError("oversized report")
        report = json.loads(result.stdout)
        if (not isinstance(report, dict) or report.get("harness") != harness
                or report.get("status") not in ("METADATA_READY", "NOT_READY", "ERROR")
                or report.get("model_requests") != 0
                or report.get("live_acceptance") != "NOT_VERIFIED"
                or result.returncode != {"METADATA_READY": 0, "NOT_READY": 2, "ERROR": 1}[report["status"]]):
            raise ValueError("invalid native report")
        return report
    except (OSError, ValueError, subprocess.TimeoutExpired):
        return {"harness": harness, "status": "ERROR", "live_acceptance": "NOT_VERIFIED",
                "model_requests": 0, "error": "native_inspection_failed"}


def main(argv=None, *, _collector=None, _native_collector=None, _harness_collector=None):
    parser = argparse.ArgumentParser(description="ตรวจ adapter ทั้ง 4 แบบในเครื่อง โดยไม่เรียกโมเดล")
    native_options = parser.add_mutually_exclusive_group()
    native_options.add_argument("--native-all", action="store_true", help="ตรวจ runtime ทั้งสี่แบบเพิ่ม โดยไม่เรียกโมเดล; อีกสามระบบโหลด source ชั่วคราว")
    native_options.add_argument("--native-codex", action="store_true", help="ตรวจ skills/hooks จาก Codex จริงเพิ่ม โดยไม่ส่ง prompt หรือแก้ trust")
    parser.add_argument("--format", choices=("text", "json"), default="text", help="รูปแบบรายงาน")
    parser.add_argument("--details", action="store_true", help="แสดงคำสั่งและ stdout/stderr ของทุกชุดทดสอบ")
    parser.add_argument("--require-live", action="store_true", help="คืน exit 2 หากยังไม่ยืนยันระบบจริง")
    parser.add_argument("--timeout", type=int, default=60, metavar="SECONDS", help="เวลาสูงสุดต่อชุดทดสอบ (1–300 วินาที)")
    args = parser.parse_args(argv)
    if not 1 <= args.timeout <= 300:
        parser.error("--timeout ต้องอยู่ระหว่าง 1 และ 300 วินาที")
    report = (_collector or collect_report)(args.timeout)
    if args.native_codex:
        report["scope"] = "offline_adapter_and_native_metadata"
        report["native_codex_metadata"] = (_native_collector or collect_native_codex)()
    if args.native_all:
        report["scope"] = "offline_adapter_and_native_metadata"
        report["native_harness_metadata"] = {
            name: (_harness_collector or collect_native_harness)(name)
            for name, _ in SUITES if name != "shared_package"}

    print(json.dumps(report, ensure_ascii=False, indent=2) if args.format == "json" else render_text(report, details=args.details))
    if report["local_status"] != "PASS":
        return 1
    statuses = [row["status"] for row in report.get("native_harness_metadata", {}).values()]
    if "native_codex_metadata" in report:
        statuses.append(report["native_codex_metadata"]["status"])
    if "ERROR" in statuses:
        return 1
    return 2 if args.require_live or "NOT_READY" in statuses else 0


if __name__ == "__main__":
    sys.exit(main())
