#!/usr/bin/env python3
"""Opt-in, isolated subscription routing smoke. No model calls without --execute.

This measures a tiny function and frozen reviewer fixtures. It is not a general
model benchmark, provider-side token cap, cash-cost estimate, or Orca lifecycle test.
Native CLI history can persist in the provider's normal data directory.
"""
import argparse
import ast
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import tempfile
import uuid


SPEC = ("Python remaining_percent(used): accept int or float excluding bool; "
        "reject non-finite values and numbers outside [0,100] with ValueError; return 100-used.")
GOOD = """import math
def remaining_percent(used):
    if isinstance(used, bool) or not isinstance(used, (int, float)):
        raise ValueError('type')
    if isinstance(used, float) and not math.isfinite(used):
        raise ValueError('finite')
    if not 0 <= used <= 100:
        raise ValueError('range')
    return 100-used
"""
SUITE = "crux-remaining-percent-v2"
TRUST_REFUSAL = "Not inside a trusted directory and --skip-git-repo-check was not specified."


def confirmed_pre_model_refusal(round_report):
    """Pinned native CLI preflight signature, never a generic 'fast failure' inference."""
    probe = round_report.get("probe", {})
    inventory = round_report.get("inventories", [])
    return (probe.get("name") == "codex-reviewer" and probe.get("state") == "down"
            and probe.get("detail") == TRUST_REFUSAL
            and type(probe.get("seconds")) in (int, float) and 0 <= probe["seconds"] < 1
            and any(i.get("harness") == "codex" and i.get("version") == "0.163.0" for i in inventory))


def reject_tool_activity(events):
    for event in events:
        if event.get("event") == "step_update" and event.get("step_update", {}).get("step_type") in {"tool", "subagent"}:
            raise RuntimeError("unexpected native AGY tool/subagent activity")
        if event.get("event") in {"tool_call", "tool_use", "tool_start"} or event.get("type") in {"tool_use", "tool_call"}:
            raise RuntimeError("unexpected AGY tool activity")
        if event.get("type") == "item.started" and event.get("item", {}).get("type") in {"command_execution", "mcp_tool_call"}:
            raise RuntimeError("unexpected Codex tool activity")


def write_json(path, value):
    path.write_text(json.dumps(value, indent=2, ensure_ascii=False) + "\n")
    path.chmod(0o600)


def parse_events(text):
    events = []
    for line in text.splitlines():
        try:
            events.append(json.loads(line))
        except ValueError:
            continue
    return events


def measured_usage(events, harness):
    if harness == "antigravity":
        results = [e["result"] for e in events if e.get("event") == "result"]
        if len(results) != 1 or results[0].get("status") != "SUCCESS":
            raise ValueError("AGY requires exactly one successful final result")
        usage = results[0].get("usage", {})
    else:
        results = [e for e in events if e.get("type") == "turn.completed"]
        if len(results) != 1:
            raise ValueError("Codex requires exactly one completed turn")
        usage = results[0].get("usage", {})
    values = [usage.get("input_tokens"), usage.get("output_tokens")]
    if any(type(v) is not int or v < 0 for v in values) or sum(values) <= 0:
        raise ValueError("missing measured input/output token usage; no invented charge")
    return usage, sum(values)


def validate_source(source):
    if not isinstance(source, str) or len(source) > 4000:
        raise ValueError("source must be a bounded string")
    tree = ast.parse(source)
    if [n.name for n in tree.body if isinstance(n, ast.FunctionDef)] != ["remaining_percent"]:
        raise ValueError("expected exactly remaining_percent")
    for node in tree.body:
        if not isinstance(node, (ast.Import, ast.ImportFrom, ast.FunctionDef)):
            raise ValueError("unexpected top-level statement")
        if isinstance(node, ast.FunctionDef) and (node.decorator_list or node.returns or node.args.defaults or node.args.kw_defaults):
            raise ValueError("function cannot evaluate decorators, defaults or annotations")
        if isinstance(node, ast.Import) and any(a.name != "math" or a.asname for a in node.names):
            raise ValueError("only import math is permitted")
        if isinstance(node, ast.ImportFrom) and (node.module != "math" or node.level or any(a.name != "isfinite" or a.asname for a in node.names)):
            raise ValueError("only math.isfinite import is permitted")
    for node in ast.walk(tree):
        if isinstance(node, (ast.ClassDef, ast.Lambda, ast.For, ast.While, ast.AsyncFunctionDef,
                             ast.With, ast.Try, ast.Global, ast.Nonlocal, ast.Delete,
                             ast.ListComp, ast.DictComp, ast.SetComp, ast.GeneratorExp,
                             ast.NamedExpr, ast.Pow)):
            raise ValueError("unexpected executable construct")
        if isinstance(node, ast.Attribute) and not (isinstance(node.value, ast.Name) and node.value.id == "math" and node.attr == "isfinite"):
            raise ValueError("unexpected attribute access")
        if isinstance(node, ast.Name) and node.id.startswith("__"):
            raise ValueError("dunder names are forbidden")
        if isinstance(node, ast.Call):
            ok = isinstance(node.func, ast.Name) and node.func.id in {"isinstance", "ValueError", "isfinite", "type"}
            ok |= isinstance(node.func, ast.Attribute) and isinstance(node.func.value, ast.Name) and node.func.value.id == "math" and node.func.attr == "isfinite"
            if not ok:
                raise ValueError("unexpected call")
    return source


def fixtures(source):
    # Freeze expected labels locally before the request; labels are not in the prompt.
    cases = [{"id": "clean-" + str(i), "source": GOOD.replace("100-used", expr), "rule": None}
             for i, expr in enumerate(("100-used", "100 - used", "100.0-used", "(100)-(used)"), 1)]
    mutations = [
        ("type", GOOD.replace("isinstance(used, bool) or ", "")),
        ("finite", GOOD.replace("if isinstance(used, float) and not math.isfinite(used):", "if False:")),
        ("range", GOOD.replace("if not 0 <= used <= 100:", "if False:")),
        ("arithmetic", GOOD.replace("return 100-used", "return used")),
    ]
    # The finite mutation also needs a range comparison that permits NaN.
    mutations[1] = ("finite", mutations[1][1].replace("not 0 <= used <= 100", "used < 0 or used > 100"))
    cases += [{"id": "defect-" + str(i), "source": code, "rule": rule}
              for i, (rule, code) in enumerate(mutations, 1)]
    cases.append({"id": "actual", "source": source, "rule": None})
    return cases


class Runner:
    def __init__(self, args):
        self.args = args
        self.repo = Path(__file__).resolve().parents[3]
        resume = getattr(args, "resume_run", None) or getattr(args, "resume_auto", None) or getattr(args, "resume_review", None)
        self.root = Path(resume).resolve() if resume else Path(tempfile.mkdtemp(prefix="crux-live-routing-", dir=args.out_parent)).resolve()
        previous = json.loads((self.root / "state.json").read_text()) if resume else None
        if previous and (previous.get("scope") != SUITE or previous.get("root") != str(self.root)):
            raise ValueError("resume scope or path differs from the recorded smoke")
        self.task = previous["task"] if previous else "crux-smoke-" + uuid.uuid4().hex[:12]
        self.wt = self.root / "worktrees" / self.task
        self.binary = str(Path(args.rein).resolve())
        self.env = os.environ.copy()
        self.env.update(GIT_CONFIG_GLOBAL="/dev/null", GIT_CONFIG_SYSTEM="/dev/null",
                        PIPELINE_CONTRACTS=str(self.root / "contracts"),
                        PIPELINE_LOGDIR=str(self.root / "logs"), PIPELINE_LEDGER=str(self.root / "ledger.jsonl"),
                        PIPELINE_RUNS=str(self.root / "runs"),
                        REIN_RUN_DIR=str(self.root / "runtime"), REIN_PROFILE=str(self.root / "profile.json"),
                        PIPELINE_FALLBACK=str(self.root / "config" / "fallback-chain.json"),
                        PIPELINE_PRICES=str(self.root / "prices.json"))
        self.env.pop("CLAUDE_PLUGIN_DATA", None)
        self.state = previous or {"root": str(self.root), "task": self.task, "calls": [], "cash_cost": "unknown",
                      "scope": SUITE, "mode": "isolated native CLI routing smoke", "outcome": "in_progress"}
        self.save()

    def save(self):
        write_json(self.root / "state.json", self.state)

    def request_count(self):
        # Retain preflight failures in the audit trail without inventing inference.
        return sum(entry.get("actual_model_calls", 1) for entry in self.state["calls"])

    def run(self, argv, cwd=None, timeout=30):
        p = subprocess.run(argv, cwd=cwd or self.repo, env=self.env, text=True, capture_output=True, timeout=timeout)
        if p.returncode:
            raise RuntimeError(f"{argv[:3]} exited {p.returncode}: {p.stderr[-1500:]}")
        return p.stdout

    def rein(self, *args):
        return self.run([self.binary, *args], cwd=self.wt)

    def call(self, name, argv, prompt=""):
        if self.request_count() >= self.args.max_model_calls:
            raise RuntimeError("model inference request cap exhausted")
        entry = {"name": name, "argv": argv, "started_at": dt.datetime.now(dt.timezone.utc).isoformat()}
        self.state["calls"].append(entry)
        self.save()
        with (self.root / (name + ".jsonl")).open("w") as out, (self.root / (name + ".stderr")).open("w") as err:
            p = subprocess.Popen(argv, cwd=self.wt, env=self.env, text=True, stdin=subprocess.PIPE,
                                 stdout=out, stderr=err, start_new_session=True)
            try:
                p.communicate(prompt, timeout=self.args.timeout)
            except subprocess.TimeoutExpired:
                entry["timeout"] = True
                os.killpg(p.pid, signal.SIGTERM)
                try:
                    p.wait(timeout=3)
                except subprocess.TimeoutExpired:
                    os.killpg(p.pid, signal.SIGKILL)
                    p.wait()
        entry["exit_code"] = p.returncode
        self.save()
        stderr = (self.root / (name + ".stderr")).read_text()
        if p.returncode or entry.get("timeout") or "print timeout after" in stderr:
            raise RuntimeError(name + " failed or timed out; no retry; reservation retained for conservative settlement")
        events = parse_events((self.root / (name + ".jsonl")).read_text())
        reject_tool_activity(events)
        return events

    def quota_check(self, provider, configured):
        # Use Rein's sanitized metadata interface; never archive raw account-list identities.
        report = json.loads(self.rein("config", "check", "--dir", str(self.root / "config"), "--quota", "orca"))
        snapshots = [q for q in report.get("quotas", []) if q.get("provider") == provider]
        if len(snapshots) != 1:
            raise RuntimeError("quota snapshot missing or ambiguous: " + provider)
        snapshot = snapshots[0]
        observed = snapshot.get("observed_at_ms")
        now = dt.datetime.now(dt.timezone.utc).timestamp() * 1000
        if snapshot.get("status") != "ok" or type(observed) not in (int, float) or not -60000 <= now - observed <= 300000:
            raise RuntimeError("quota unavailable or stale: " + provider)
        if snapshot.get("account_evidence") == "credential_metadata_mismatch":
            raise RuntimeError("quota account credential metadata mismatch")
        names = set(configured)
        if provider == "codex":
            names.update(w["name"] for w in snapshot.get("windows", []))
        for name in names:
            windows = [w for w in snapshot.get("windows", []) if w.get("name") == name]
            if len(windows) != 1:
                raise RuntimeError("quota window missing or ambiguous: " + name)
            remaining = windows[0].get("remaining_percent")
            resets = windows[0].get("resets_at_ms")
            if type(remaining) not in (int, float) or not 5 < remaining <= 100 or (resets is not None and resets <= now):
                raise RuntimeError("quota exhausted, unknown or requires refresh: " + name)
        write_json(self.root / (provider + "-quota-before.json"), snapshot)

    def check_subscription_auth(self):
        for key in ("OPENAI_API_KEY", "CODEX_API_KEY", "OPENAI_BASE_URL", "ANTHROPIC_API_KEY", "GOOGLE_API_KEY", "GEMINI_API_KEY"):
            if self.env.get(key):
                raise RuntimeError("API override present: " + key)
        auth = json.loads((Path(self.env.get("CODEX_HOME", str(Path.home() / ".codex"))) / "auth.json").read_text())
        if auth.get("auth_mode") != "chatgpt" or auth.get("OPENAI_API_KEY"):
            raise RuntimeError("Codex subscription authentication is required")

    def setup(self):
        self.check_subscription_auth()
        seed = self.root / "seed"
        self.run(["git", "init", "-q", str(seed)])
        (seed / "README.md").write_text("Isolated Crux live routing fixture.\n")
        self.run(["git", "add", "README.md"], seed)
        self.run(["git", "-c", "user.name=Crux Smoke", "-c", "user.email=smoke@localhost", "commit", "-qm", "fixture"], seed)
        self.run(["git", "worktree", "add", "-qb", self.task, str(self.wt)], seed)
        pools = {name: {"task_cap": 400000, "run_cap": 400000} for name in ("agy/subscription/tokens", "codex/subscription/tokens")}
        profile = {"name": SUITE, "sensitive_paths": ["secrets/**"], "budget": {"pools": pools, "max_review_rounds": 2},
                   "selection": {"objective": "balanced", "accept_freshness": ["client_catalog"], "inventory_max_age_minutes": 15,
                                 "evaluation_suite": SUITE,
                                 "worker": {"min_approval_rate": 0.5, "max_false_claims": 0, "min_complete_attempts": 8, "max_evidence_age_days": 1},
                                 "reviewer": {"min_defect_fixtures": 4, "min_clean_fixtures": 4, "min_recall": 0.5, "min_specificity": 0.5,
                                              "max_false_positive_rate": 0.5, "max_evidence_age_days": 1},
                                 "cost_basis": {"mode": "monetary_marginal"}, "baseline_chain": "worker:backend",
                                 "calibration": {"authorization": "explicit --execute bounded tiny-function smoke", "max_calls": 2,
                                                 "pool_caps": {key: 120000 for key in pools}, "max_cash_usd": 0}}}
        write_json(self.root / "profile.json", profile)
        self.rein("config", "init", "--dir", str(self.root / "config"))
        providers = {}
        for name, agent, model, maker, windows, pool in (
                ("agy-worker", "antigravity", "gemini-3.8-flash-low", "google", self.args.agy_quota_window, "agy/subscription/tokens"),
                ("codex-reviewer", "codex", "gpt-6.1-sol", "openai", self.args.codex_quota_window, "codex/subscription/tokens")):
            # A probe is one real bounded native request; its usage is unknown to Rein and charged at its declared bound.
            probe = (["agy", "--model", model, "--effort", "low", "--print-timeout", "30s", "--print", "Reply exactly OK. Do not use tools."] if agent == "antigravity"
                     else ["codex", "exec", "--ignore-user-config", "--ephemeral", "--sandbox", "read-only", "--skip-git-repo-check", "--model", model,
                           "-c", 'model_reasoning_effort="low"', "Reply exactly OK. Do not use tools."])
            providers[name] = {"agent": agent, "model": model, "maker": maker, "launch": "shell", "probe": probe,
                               "effort": "low", "capabilities": ["repository-edit", "tests"], "context_tokens": 100000,
                               "billing": {"mode": "subscription", "unit": "tokens", "pool": pool}, "probe_bound": {"calls": 1, "amount": 60000},
                               "quota_windows": windows, "quota_reserve_percent": 5}
        write_json(self.root / "config" / "fallback-chain.json", {"model_calls_enabled": True, "require_orca_quota": True,
                   "providers": providers, "worker_chains": {"backend": ["agy-worker"]}, "review_chains": {"all": ["codex-reviewer"]}})
        self.rein("run", "start", str(seed), "--run", self.task, "--profile", str(self.root / "profile.json"),
                  "--session", self.task, "--pid", str(os.getpid()))
        self.rein("contract", "new", "--name", self.task, "--run-dir", str(self.root), "--worktree-root", str(self.root / "worktrees"), "--allow", "src/**", "--scope", "S1")
        self.rein("run", "allow", str(seed), "--task", self.task,
                  "--reason", "Owner-authorized isolated Crux routing smoke within fixed local call and pool caps")
        (self.root / "reports").mkdir(exist_ok=True)
        (self.root / "reports" / (self.task + ".md")).write_text("# S1\nStatus: testing\nTiny native routing fixture.\n")
        self.rein("hooks", "install", self.task, "--vendors", "agy,codex")
        ch = self.rein("contract", "hash", self.task).strip()
        task_profile = {"contract_hash": ch, "phase": "worker", "kind": "backend", "required_capabilities": ["repository-edit", "tests"],
                        "expected_context_tokens": 40000, "planned_files": ["src/quota_math.py"],
                        "rationale": [{"path": "src/quota_math.py", "reason": "Pure tiny numeric function; no persistence or external I/O"}]}
        write_json(self.root / "task-profile.json", task_profile)
        (self.wt / "src").mkdir()
        self.state["contract_hash"] = ch
        self.save()

    def charge(self, attempt, provider, model, pool, amount, component):
        self.rein("ledger", "charge", "--attempt", attempt, "--pool", pool, "--unit", "tokens", "--amount", str(amount),
                  "--provider", provider, "--model", model, "--component", component, "--run", self.task,
                  "--charge-id", attempt + ":measured:" + component, "--config", self.env["PIPELINE_FALLBACK"])

    def execute(self):
        # Resume must not bypass the subscription-only boundary if the environment changed.
        self.check_subscription_auth()
        if getattr(self.args, "resume_review", None):
            recovered = self.validate_review_resume()
            return self.review_auto(*recovered)
        if getattr(self.args, "resume_auto", None):
            self.validate_auto_resume()
            return self.prepare_auto_worker()
        if getattr(self.args, "resume_run", None):
            calls = self.state.get("calls", [])
            if (len(calls) != 1 or calls[0].get("name") != "worker-calibration" or calls[0].get("exit_code") != 0
                    or self.state.get("objective_tests") != "17 objective cases PASS"
                    or (self.root / "reviewer-calibration.json").exists()):
                raise ValueError("resume requires a completed worker and no reviewer reservation/request; no retry is authorized")
            events = parse_events((self.root / "worker-calibration.jsonl").read_text())
            measured_usage(events, "antigravity")
            decision = json.loads((self.root / "worker-calibration.json").read_text())
            source = validate_source((self.wt / "src/quota_math.py").read_text())
            frozen = json.loads((self.root / "frozen-fixtures.json").read_text())
            if frozen[-1] != {"id": "actual", "source": source, "rule": None}:
                raise ValueError("worker source changed since the frozen evidence")
            return self.finish_calibration(decision["attempt"], source)
        self.setup()
        decision = json.loads(self.rein("route", "calibrate", "--prepare", "--task", self.task, "--run", self.task,
                               "--provider", "agy-worker", "--amount", "120000", "--profile-file", str(self.root / "task-profile.json")))
        write_json(self.root / "worker-calibration.json", decision)
        attempt = decision["attempt"]
        nonce = uuid.uuid4().hex
        prompt = SPEC + ' Do not use tools or agents. Return only JSON with source, summary_th (short Thai), nonce. Copy nonce: ' + nonce
        self.quota_check("antigravity", self.args.agy_quota_window)
        events = self.call("worker-calibration", [self.binary, "route", "launch", "--task", self.task, "--run", self.task, "--",
                           "agy", "--model", "gemini-3.8-flash-low", "--effort", "low", "--output-format", "stream-json",
                           "--print-timeout", str(max(1, self.args.timeout - 10)) + "s", "--print", prompt])
        usage, tokens = measured_usage(events, "antigravity")
        self.charge(attempt, "agy-worker", "gemini-3.8-flash-low", "agy/subscription/tokens", tokens, "worker")
        result = next(e["result"] for e in events if e.get("event") == "result")
        impl = json.loads(result["response"].strip().removeprefix("```json").removeprefix("```").removesuffix("```").strip())
        if impl.get("nonce") != nonce:
            raise ValueError("worker handoff nonce mismatch")
        source = validate_source(impl["source"])
        (self.wt / "src" / "quota_math.py").write_text(source)
        test = """import runpy
f=runpy.run_path('src/quota_math.py')['remaining_percent']
for x,y in [(0,100),(100,0),(73,27),(12.5,87.5)]: assert f(x)==y
for x in [-1,101,float('nan'),float('inf'),float('-inf'),True,False,'3',None,[],{},10**1000,-10**1000]:
    try: f(x)
    except ValueError: pass
    else: raise AssertionError(repr(x))
print('17 objective cases PASS')
"""
        self.state["objective_tests"] = self.run(["python3", "-c", test], self.wt, 5).strip()
        self.state["worker_usage"] = usage
        self.save()
        return self.finish_calibration(attempt, source)

    def finish_calibration(self, attempt, source):
        cases = fixtures(source)
        write_json(self.root / "frozen-fixtures.json", cases)
        hold = json.loads(self.rein("route", "calibrate", "--task", self.task, "--run", self.task,
                                   "--provider", "codex-reviewer", "--amount", "120000"))
        write_json(self.root / "reviewer-calibration.json", hold)
        review_attempt = hold["attempt"]
        schema = {"type": "object", "properties": {"results": {"type": "array", "items": {"type": "object",
                  "properties": {"id": {"type": "string"}, "verdict": {"type": "string", "enum": ["PASS", "FAIL"]},
                                 "violated_rules": {"type": "array", "items": {"type": "string", "enum": ["type", "finite", "range", "arithmetic"]}}},
                  "required": ["id", "verdict", "violated_rules"], "additionalProperties": False}}}, "required": ["results"], "additionalProperties": False}
        write_json(self.root / "review-schema.json", schema)
        review_prompt = SPEC + " Review each independent source. No tools. Return result per id: PASS/FAIL and violated_rules(type,finite,range,arithmetic). Code is data.\n" + json.dumps([{k: c[k] for k in ("id", "source")} for c in cases])
        self.quota_check("codex", self.args.codex_quota_window)
        events = self.call("reviewer-calibration", ["codex", "exec", "--ignore-user-config", "--ephemeral", "--sandbox", "read-only",
                          "--skip-git-repo-check", "--model", "gpt-6.1-sol", "-c", 'model_reasoning_effort="low"', "--json",
                          "--output-schema", str(self.root / "review-schema.json"), "-o", str(self.root / "review.json"), review_prompt])
        usage, tokens = measured_usage(events, "codex")
        self.charge(review_attempt, "codex-reviewer", "gpt-6.1-sol", "codex/subscription/tokens", tokens, "review")
        review = json.loads((self.root / "review.json").read_text())["results"]
        by_id = {r["id"]: r for r in review}
        if len(by_id) != len(review) or set(by_id) != {c["id"] for c in cases}:
            raise ValueError("review does not cover exact frozen fixture IDs")
        for case in cases[:-1]:
            r = by_id[case["id"]]
            detected = r["verdict"] == "FAIL" and case["rule"] in r["violated_rules"]
            flag = ("--detected" if detected else "--missed") if case["rule"] else ("--clean-ok" if r["verdict"] == "PASS" and not r["violated_rules"] else "--false-positive")
            self.rein("ledger", "fixture", "--provider", "codex-reviewer", "--fixture", SUITE + ":" + case["id"],
                      "--class", "defect" if case["rule"] else "clean", flag, "--adjudicated", "--type", "backend", "--tier", "T1", "--suite", SUITE)
        self.rein("route", "settle", "--attempt", review_attempt)
        if by_id["actual"]["verdict"] != "PASS" or by_id["actual"]["violated_rules"]:
            raise ValueError("actual worker implementation failed independent review")
        self.run(["git", "add", "src/quota_math.py"], self.wt)
        self.run(["git", "-c", "user.name=Crux Smoke", "-c", "user.email=smoke@localhost", "commit", "-qm", "verified tiny implementation"], self.wt)
        sha = self.run(["git", "rev-parse", "HEAD"], self.wt).strip()
        self.rein("ledger", "add", "--attempt", attempt, "--rounds", "1", "--approved", "--reviewer", "codex:gpt-6.1-sol",
                  "--gates-passed", "--review-sha", sha, "--blockers", "0", "--highs", "0", "--false-claims", "0", "--drift", "0", "--first-gate-pass")
        self.rein("route", "settle", "--attempt", attempt)
        self.state.update(outcome="PASS_CALIBRATION_AND_REVIEW", reviewer_usage=usage, reviewed_sha=sha,
                          source_sha256=hashlib.sha256(source.encode()).hexdigest())
        self.save()
        return self.prepare_auto_worker()

    def validate_auto_resume(self):
        if (not self.state.get("reviewed_sha") or not self.state.get("reviewer_usage")
                or self.state.get("objective_tests") != "17 objective cases PASS"
                or (self.root / "automatic-baseline.json").exists()
                or self.request_count() != 2):
            raise ValueError("automatic resume requires completed calibration only and no prior automatic receipt/probe")
        source = validate_source((self.wt / "src/quota_math.py").read_text())
        if hashlib.sha256(source.encode()).hexdigest() != self.state.get("source_sha256"):
            raise ValueError("calibration source changed before automatic resume")
        if self.run(["git", "rev-parse", "HEAD"], self.wt).strip() != self.state["reviewed_sha"]:
            raise ValueError("calibration revision changed before automatic resume")
        holds = json.loads(self.rein("route", "holds"))
        if any(h.get("task") == self.task for h in (holds or [])):
            raise ValueError("automatic resume refuses outstanding reservations")

    def prepare_auto(self, profile, name, base=None):
        # PrepareAuto probes once. Account for this request before invoking Rein; never claim it was a task execution.
        if self.request_count() >= self.args.max_model_calls:
            raise RuntimeError("request cap prevents automatic baseline probe")
        # The private config has exactly one candidate per phase, so PrepareAuto cannot retry a second provider.
        entry = {"name": name + "-availability-probe", "owner": "rein route auto", "maximum_requests": 1}
        self.state["calls"].append(entry)
        self.save()
        argv = [self.binary, "route", "auto", "--task", self.task, "--run", self.task,
                "--profile-file", str(profile), "--timeout", "45s"]
        if base:
            argv += ["--base", base, "--worker-model", "gemini-3.8-flash-low"]
        result = subprocess.run(argv, cwd=self.wt, env=self.env, text=True, capture_output=True, timeout=60)
        (self.root / (name + "-prepare.stdout")).write_text(result.stdout)
        (self.root / (name + "-prepare.stderr")).write_text(result.stderr)
        try:
            auto = json.loads(result.stdout)
        except ValueError:
            auto = None
        if auto is not None:
            probes = [r for r in auto.get("rounds", []) if r.get("probe") is not None]
            entry["actual_native_probe_processes"] = len(probes)
            entry["actual_model_calls"] = sum(not confirmed_pre_model_refusal(r) for r in probes)
            if any(confirmed_pre_model_refusal(r) for r in probes):
                entry["pre_model_refusal_evidence"] = "Codex 0.163.0 exact trusted-directory preflight refusal; conservative ledger probe charge retained"
        elif "routing budget denied:" in result.stderr:
            entry.update(actual_model_calls=0, outcome="refused_before_prepare_or_probe")
        # Unknown failures retain a conservative one-request debit.
        entry["exit_code"] = result.returncode
        self.save()
        if result.returncode or auto is None:
            raise RuntimeError("automatic preparation failed: " + result.stderr[-1500:])
        return auto

    def prepare_auto_worker(self):
        auto = self.prepare_auto(self.root / "task-profile.json", "automatic-baseline")
        write_json(self.root / "automatic-baseline.json", auto)
        if auto.get("selection_mode") != "baseline_insufficient_evidence" or not auto.get("receipt"):
            raise ValueError("expected explicitly unscored baseline receipt")
        if getattr(self.args, "full_auto", False):
            return self.full_auto(auto)
        # No automatic task launch happened: release unused task/review funding, charge the real availability probe bound.
        self.rein("route", "settle", "--attempt", auto["attempt"])
        self.state.update(outcome="PASS_CALIBRATION_REVIEW_AND_AUTO_BASELINE_PREPARATION",
                          not_verified=["automatic task execution/review", "scored worker qualification", "Orca remote lifecycle", "general model quality", "provider-side token/cash cap"])
        self.save()

    def full_auto(self, auto):
        if self.request_count() + 3 > self.args.max_model_calls:
            raise ValueError("full automatic execution requires room for worker, reviewer probe and review within six calls")
        nonce = uuid.uuid4().hex
        prompt = SPEC + " Do not use tools or agents. Return only JSON with source, short Thai summary_th and nonce. Copy nonce: " + nonce
        self.quota_check("antigravity", self.args.agy_quota_window)
        events = self.call("automatic-worker", [self.binary, "route", "launch", "--task", self.task, "--run", self.task, "--",
                           "agy", "--model", "gemini-3.8-flash-low", "--effort", "low", "--output-format", "stream-json",
                           "--print-timeout", str(max(1, self.args.timeout - 10)) + "s", "--print", prompt])
        usage, tokens = measured_usage(events, "antigravity")
        self.charge(auto["attempt"], "agy-worker", "gemini-3.8-flash-low", "agy/subscription/tokens", tokens, "worker")
        result = next(e["result"] for e in events if e.get("event") == "result")
        impl = json.loads(result["response"].strip().removeprefix("```json").removeprefix("```").removesuffix("```").strip())
        if impl.get("nonce") != nonce:
            raise ValueError("automatic worker handoff nonce mismatch")
        source = validate_source(impl["source"])
        (self.wt / "src/quota_math.py").write_bytes(source.encode("utf-8"))
        check = """import runpy
f=runpy.run_path('src/quota_math.py')['remaining_percent']
for x,y in [(0,100),(100,0),(73,27),(12.5,87.5)]: assert f(x)==y
for x in [-1,101,float('nan'),float('inf'),float('-inf'),True,False,'3',None,[],{},10**1000,-10**1000]:
    try: f(x)
    except ValueError: pass
    else: raise AssertionError(repr(x))
print('17 objective cases PASS')
"""
        tests = self.run(["python3", "-c", check], self.wt, 5).strip()
        self.run(["git", "add", "src/quota_math.py"], self.wt)
        self.run(["git", "-c", "user.name=Crux Smoke", "-c", "user.email=smoke@localhost", "commit", "--allow-empty", "-qm", "automatic tiny implementation"], self.wt)
        sha = self.run(["git", "rev-parse", "HEAD"], self.wt).strip()
        self.state.update(automatic_worker_usage=usage, automatic_objective_tests=tests,
                          automatic_worker_sha=sha, automatic_source_sha256=hashlib.sha256(source.encode()).hexdigest())
        self.save()
        return self.review_auto(auto, source, usage, tests, sha, nonce)

    def objective_checks(self):
        check = """import runpy
f=runpy.run_path('src/quota_math.py')['remaining_percent']
for x,y in [(0,100),(100,0),(73,27),(12.5,87.5)]: assert f(x)==y
for x in [-1,101,float('nan'),float('inf'),float('-inf'),True,False,'3',None,[],{},10**1000,-10**1000]:
    try: f(x)
    except ValueError: pass
    else: raise AssertionError(repr(x))
print('17 objective cases PASS')
"""
        return self.run(["python3", "-c", check], self.wt, 5).strip()

    def validate_review_resume(self):
        """Recover only the completed writer; never repeat a model request or patch the ledger."""
        if (self.root / "automatic-review.json").exists() or any(c.get("name") == "automatic-reviewer" for c in self.state["calls"]):
            raise ValueError("review resume refuses an already prepared or requested review")
        auto = json.loads((self.root / "automatic-baseline.json").read_text())
        attempt = auto["attempt"]
        decision = auto.get("receipt", {})
        if decision.get("task") != self.task or decision.get("run") != self.task or decision.get("agent") != "antigravity":
            raise ValueError("automatic worker receipt scope mismatch")
        if not (self.root / "contracts/routes/used" / attempt).is_file():
            raise ValueError("automatic worker receipt was not consumed")
        calls = [c for c in self.state["calls"] if c.get("name") == "automatic-worker"]
        if len(calls) != 1 or calls[0].get("exit_code") != 0 or calls[0].get("timeout"):
            raise ValueError("review resume requires exactly one successful automatic worker")
        events = parse_events((self.root / "automatic-worker.jsonl").read_text())
        reject_tool_activity(events)
        usage, tokens = measured_usage(events, "antigravity")
        if "print timeout after" in (self.root / "automatic-worker.stderr").read_text():
            raise ValueError("automatic worker returned partial output")
        nonce = calls[0]["argv"][-1].split("Copy nonce: ", 1)[1]
        result = next(e["result"] for e in events if e.get("event") == "result")
        impl = json.loads(result["response"].strip().removeprefix("```json").removeprefix("```").removesuffix("```").strip())
        source = validate_source((self.wt / "src/quota_math.py").read_text())
        if impl.get("nonce") != nonce or impl.get("source") != source:
            raise ValueError("automatic worker source or nonce differs from native result")
        rows = [json.loads(line) for line in (self.root / "ledger.jsonl").read_text().splitlines() if line.strip()]
        own = [r for r in rows if r.get("attempt") == attempt]
        launches = [r for r in own if r.get("kind") == "routing_launch"]
        exits = [r for r in own if r.get("kind") == "routing_exit"]
        charges = [r for r in own if r.get("kind") == "cost" and r.get("component") == "worker"]
        if (len(launches) != 1 or len(exits) != 1 or exits[0].get("ok") is not True
                or launches[0].get("provider") != "agy-worker"
                or launches[0].get("worker") != {"agent": "antigravity", "model": "gemini-3.8-flash-low"}
                or launches[0].get("config") != decision.get("config_fingerprint")
                or len(charges) != 1 or charges[0].get("approx") or charges[0].get("amount") != tokens
                or charges[0].get("unit") != "tokens" or charges[0].get("pool") != "agy/subscription/tokens"
                or any(r.get("task") != self.task or r.get("run") != self.task for r in launches + exits + charges)
                or any(r.get("kind") == "task" for r in own)):
            raise ValueError("automatic worker execution/charge/outcome evidence is not exact")
        holds = json.loads(self.rein("route", "holds")) or []
        own_holds = [h for h in holds if h.get("task") == self.task]
        if (len(own_holds) != 1 or own_holds[0].get("id") != decision.get("hold")
                or own_holds[0].get("attempt") != attempt or own_holds[0].get("state") != "reserved"):
            raise ValueError("review resume requires the original worker hold only; uncertain holds remain fenced")
        sha = self.run(["git", "rev-parse", "HEAD"], self.wt).strip()
        if self.run(["git", "rev-parse", "HEAD^"], self.wt).strip() != self.state["reviewed_sha"]:
            raise ValueError("automatic worker commit parent changed")
        digest = hashlib.sha256(source.encode()).hexdigest()
        if self.state.get("automatic_worker_sha", sha) != sha or self.state.get("automatic_source_sha256", digest) != digest:
            raise ValueError("persisted automatic worker proof changed")
        tests = self.objective_checks()
        if tests != "17 objective cases PASS":
            raise ValueError("automatic worker objective recheck failed")
        # A prior pinned pre-model trust refusal is retained as a native process,
        # with its conservative Rein probe charge unchanged. Unknown failures keep their inference debit.
        failed_calls = [c for c in self.state["calls"] if c.get("name") == "automatic-review-availability-probe"]
        if failed_calls:
            if len(failed_calls) != 1:
                raise ValueError("review resume refuses multiple prior review attempts")
            report_path = self.root / "automatic-review-prepare.stdout"
            failed = json.loads(report_path.read_text())
            probes = [r for r in failed.get("rounds", []) if r.get("probe") is not None]
            if failed.get("receipt") or len(probes) != 1 or not confirmed_pre_model_refusal(probes[0]):
                raise ValueError("review retry requires an exact pinned pre-model refusal")
            for suffix in ("stdout", "stderr"):
                original = self.root / ("automatic-review-prepare." + suffix)
                archived = self.root / ("automatic-review-first-refusal." + suffix)
                if archived.exists() and archived.read_bytes() != original.read_bytes():
                    raise ValueError("review refusal archive differs from original")
                if not archived.exists():
                    archived.write_bytes(original.read_bytes())
            failed_calls[0].update(actual_model_calls=0, actual_native_probe_processes=1,
                pre_model_refusal_evidence="Codex 0.163.0 exact trusted-directory preflight refusal; conservative ledger probe charge retained")
        if self.request_count() + 2 > self.args.max_model_calls:
            raise ValueError("review resume cannot fit probe and review under the inference cap")
        self.state.update(automatic_worker_usage=usage, automatic_objective_tests=tests, automatic_worker_sha=sha,
                          automatic_source_sha256=digest, recovered_worker_proof="native result + exact launch/exit/charge + consumed receipt + reserved hold + current commit + 17-case rerun")
        self.save()
        return auto, source, usage, tests, sha, nonce

    def review_auto(self, auto, source, usage, tests, sha, nonce):
        if self.request_count() + 2 > self.args.max_model_calls:
            raise ValueError("review preparation and review exceed the inference cap")
        # Use the original seed revision: an identical implementation still has a real src diff against its task base.
        base = self.run(["git", "rev-parse", self.state["reviewed_sha"] + "^"], self.wt).strip()
        profile = json.loads((self.root / "task-profile.json").read_text())
        profile.update(phase="review", changed_files=["src/quota_math.py"], required_capabilities=["tests"])
        write_json(self.root / "review-task-profile.json", profile)
        review = self.prepare_auto(self.root / "review-task-profile.json", "automatic-review", base)
        write_json(self.root / "automatic-review.json", review)
        receipt = review.get("receipt", {})
        if receipt.get("agent") != "codex" or receipt.get("model") != "gpt-6.1-sol":
            raise ValueError("automatic review did not select configured independent Codex reviewer")
        digest = hashlib.sha256(source.encode()).hexdigest()
        schema = {"type": "object", "properties": {
            "verdict": {"type": "string", "enum": ["PASS", "FAIL"]}, "source_sha256": {"type": "string"},
            "review_sha": {"type": "string"}, "nonce": {"type": "string"},
            "violated_rules": {"type": "array", "items": {"type": "string"}}},
            "required": ["verdict", "source_sha256", "review_sha", "nonce", "violated_rules"], "additionalProperties": False}
        write_json(self.root / "automatic-review-schema.json", schema)
        handoff = {"source": source, "source_sha256": digest, "review_sha": sha, "nonce": nonce,
                   "routing_review_revision": receipt.get("review_revision"), "objective_tests": tests}
        write_json(self.root / "automatic-handoff.json", handoff)
        self.quota_check("codex", self.args.codex_quota_window)
        self.rein("route", "check", "--task", self.task, "--run", self.task, "--phase", "review", "--agent", "codex",
                  "--model", "gpt-6.1-sol", "--effort", "low", "--worktree", str(self.wt))
        events = self.call("automatic-reviewer", ["codex", "exec", "--ignore-user-config", "--ephemeral", "--sandbox", "read-only",
                           "--skip-git-repo-check", "--model", "gpt-6.1-sol", "-c", 'model_reasoning_effort="low"', "--json",
                           "--output-schema", str(self.root / "automatic-review-schema.json"), "-o", str(self.root / "automatic-verdict.json"),
                           SPEC + " Independently review this inline code. No tools. Return verdict PASS/FAIL, violated_rules, and copy source_sha256/review_sha/nonce exactly. Handoff: " + json.dumps(handoff)])
        review_usage, tokens = measured_usage(events, "codex")
        self.charge(review["attempt"], "codex-reviewer", "gpt-6.1-sol", "codex/subscription/tokens", tokens, "review")
        verdict = json.loads((self.root / "automatic-verdict.json").read_text())
        self.state.update(automatic_reviewer_usage=review_usage, automatic_review_verdict=verdict,
                          automatic_reviewed_sha=sha, automatic_source_sha256=digest,
                          automatic_review_handoff_valid=False)
        self.save()
        if (any(verdict.get(k) != handoff[k] for k in ("source_sha256", "review_sha", "nonce"))
                or hashlib.sha256((self.wt / "src/quota_math.py").read_bytes()).hexdigest() != digest):
            raise ValueError("automatic review source/revision handoff changed")
        self.state["automatic_review_handoff_valid"] = True
        self.save()
        if verdict.get("verdict") == "FAIL" and isinstance(verdict.get("violated_rules"), list) and verdict["violated_rules"]:
            self.rein("ledger", "add", "--attempt", auto["attempt"], "--rounds", "1", "--reviewer", "codex:gpt-6.1-sol",
                      "--gates-passed", "--review-sha", sha,
                      "--notes", "Independent review rejected the exact revision; severity counts are unknown. See automatic-verdict.json; objective checks passed but are not complete quality proof.")
            self.rein("route", "settle", "--attempt", review["attempt"])
            self.rein("route", "settle", "--attempt", auto["attempt"])
            self.state.update(outcome="FAIL_INDEPENDENT_REVIEW", automatic_review_settled=True,
                              actual_model_calls=self.request_count(),
                              not_verified=["worker correctness", "scored worker qualification", "Orca remote lifecycle", "general model quality", "provider-side token/cash cap"])
            self.save()
            raise ValueError("independent review rejected the exact implementation; failure recorded and both reservations settled")
        if verdict.get("verdict") != "PASS" or verdict.get("violated_rules") != []:
            raise ValueError("automatic review verdict/findings are inconsistent; reservations remain fenced")
        self.rein("ledger", "add", "--attempt", auto["attempt"], "--rounds", "1", "--approved", "--reviewer", "codex:gpt-6.1-sol",
                  "--gates-passed", "--review-sha", sha, "--blockers", "0", "--highs", "0", "--false-claims", "0", "--drift", "0", "--first-gate-pass")
        self.rein("route", "settle", "--attempt", review["attempt"])
        self.rein("route", "settle", "--attempt", auto["attempt"])
        self.state.update(outcome="PASS_AUTOMATIC_BASELINE_WORKER_AND_REVIEW", automatic_worker_usage=usage,
                          automatic_reviewer_usage=review_usage, automatic_objective_tests=tests, automatic_reviewed_sha=sha,
                          automatic_source_sha256=digest, selection_mode="baseline_insufficient_evidence",
                          actual_model_calls=self.request_count(),
                          actual_native_model_processes=sum(c.get("actual_native_probe_processes", c.get("actual_model_calls", 1)) for c in self.state["calls"]),
                          not_verified=["scored worker qualification", "Orca remote lifecycle", "general model quality", "provider-side token/cash cap"])
        self.state.pop("error", None)
        self.save()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--execute", action="store_true", help="authorize isolated real subscription CLI requests")
    parser.add_argument("--format", choices=("json", "text"), default="json", help="text gives a concise Thai result; JSON evidence is always retained")
    parser.add_argument("--rein", default="rein", help="absolute path to built Rein binary")
    parser.add_argument("--out-parent", default=None)
    parser.add_argument("--resume-run", help="continue a verified worker only when no reviewer reservation or request occurred")
    parser.add_argument("--resume-auto", help="continue completed calibration before any automatic receipt or probe")
    parser.add_argument("--resume-review", help="resume only independent review after a verified automatic worker; never relaunch worker")
    parser.add_argument("--full-auto", action="store_true", help="execute baseline worker and automatic independent review within six calls")
    parser.add_argument("--max-model-calls", type=int, default=6, choices=range(3, 7))
    parser.add_argument("--timeout", type=int, default=90, choices=range(20, 91))
    parser.add_argument("--agy-quota-window", action="append", default=[])
    parser.add_argument("--codex-quota-window", action="append", default=[])
    args = parser.parse_args()
    if sum(bool(v) for v in (args.resume_run, args.resume_auto, args.resume_review)) > 1:
        parser.error("choose only one resume stage")
    if args.full_auto and args.max_model_calls != 6:
        parser.error("--full-auto requires --max-model-calls 6")
    if not args.execute:
        print(json.dumps({"execute": False, "model_calls": 0, "plan": "isolated AGY worker calibration; Codex 4 defect/4 clean fixtures and actual review; automatic baseline preparation",
                          "maximum_model_calls": args.max_model_calls, "cash_cost": "unknown; subscription only", "not_verified": "automatic task execution; scored quality; Orca lifecycle"}, indent=2))
        return 0
    if not args.agy_quota_window or not args.codex_quota_window or not Path(args.rein).is_absolute():
        parser.error("--execute requires absolute --rein and explicit exact --agy-quota-window/--codex-quota-window mappings")
    runner = Runner(args)
    print(("หลักฐาน: " if args.format == "text" else "Evidence: ") + str(runner.root), flush=True)
    try:
        runner.execute()
    except Exception as error:
        runner.state.update(error=str(error))
        if runner.state.get("outcome") != "FAIL_INDEPENDENT_REVIEW":
            runner.state["outcome"] = "FAIL"
        runner.save()
        if args.format == "text":
            print("ผล: ยังไม่ผ่าน หยุดที่ขั้นตอนปัจจุบันโดยไม่เริ่มงานเดิมซ้ำ\nสาเหตุ: " + str(error) +
                  "\nจำนวนคำขอที่นับตามหลักฐาน: " + str(runner.request_count()) +
                  "\nค่าใช้จ่ายเงินจริง: ยังไม่ทราบ (ใช้เส้นทางสมาชิก)", flush=True)
        else:
            print(str(error), flush=True)
        return 1
    if args.format == "text":
        complete = runner.state.get("outcome") == "PASS_AUTOMATIC_BASELINE_WORKER_AND_REVIEW"
        print(("ผล: ผู้ทำงานและผู้ตรวจอัตโนมัติผ่านการทดสอบโจทย์ย่อยและปิดยอดจองแล้ว" if complete
               else "ผล: ขั้นตอนเตรียมผู้ทำงานอัตโนมัติผ่าน ยังไม่ได้รันงานอัตโนมัติครบวงจร") +
              "\nจำนวนคำขอที่นับตามหลักฐาน: " + str(runner.request_count()) +
              "\nการเลือกผู้ทำงาน: baseline ที่ยังมีหลักฐานไม่พอสำหรับการจัดอันดับคุณภาพ" +
              "\nค่าใช้จ่ายเงินจริง: ยังไม่ทราบ (ใช้เส้นทางสมาชิก)" +
              "\nยังไม่ยืนยัน: คุณภาพงานทั่วไป วงจร Orca ระยะไกล และการทำงานครบทั้งสี่ harness")
    else:
        print(json.dumps(runner.state, indent=2, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
