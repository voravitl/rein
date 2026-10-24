"""Crux session mode for Hermes Agent.

The adapter keeps the Crux persistent mode working on Hermes with the same rules the
other harnesses use: activation is an explicit user command, the state is per session,
and the mode only changes the conversation policy — it never authorizes extra work,
recursive delegation, external writes or an unattended loop.

Hermes has no plugin data directory of its own, so the state root follows the same
convention as the rest of Crux (the adapter's own directory by default). It does not
read or write another harness's state.
"""

from __future__ import annotations

import hashlib
import json
import os
import re
from pathlib import Path
from typing import Any, Dict, Optional

_MODE_MARKER = "crux persistent mode"
_ADAPTER_ROOT = Path(__file__).resolve().parent
_PLUGIN_ROOT = _ADAPTER_ROOT.parent.parent

# Crux commands: `$crux:crux-mode` (Codex) and `/crux:crux-mode` (Claude).
_ON = re.compile(r"^(?:\$crux:crux-mode|/(?:crux:)?crux-mode)(?=\s|$)", re.IGNORECASE)
_OFF = re.compile(r"^(?:(?:\$crux:crux-mode|/(?:crux:)?crux-mode)\s+off|/crux-off)\s*[.!]?$", re.IGNORECASE)


def _state_root() -> Path:
    """Where this session's mode flag lives (default: the adapter's own state directory)."""
    for env in ("CRUX_STATE_DIR", "PLUGIN_DATA"):
        value = os.environ.get(env)
        if value:
            return Path(value)
    return _ADAPTER_ROOT / "state"


def _state_path(session_id: str) -> Optional[Path]:
    if not isinstance(session_id, str) or not session_id.strip() or len(session_id) > 256:
        return None
    return _state_root() / (hashlib.sha256(session_id.encode("utf-8")).hexdigest() + ".json")


def _read_active(path: Path) -> bool:
    try:
        return json.loads(path.read_text(encoding="utf-8")).get("active") is True
    except FileNotFoundError:
        return False
    except (OSError, ValueError):
        # A corrupted state file must not turn the mode on by accident.
        return False


def _activation(prompt: Any) -> Optional[bool]:
    if not isinstance(prompt, str):
        return None
    text = prompt.strip()
    if _OFF.match(text):
        return False
    return True if _ON.match(text) else None


def _mode_context() -> str:
    root = _PLUGIN_ROOT
    return (
        "crux persistent mode is active for this session. User-facing output defaults to clear Thai; "
        "honor an explicit different language for its scope. Preserve exact commands and error text. "
        f"Read {root}/skills/thai-communication/SKILL.md and {root}/references/communication.md. "
        f"Before task work, read {root}/references/runtime.md and {root}/skills/crux-mode/SKILL.md. "
        "Follow the mode coordination sequence for ADLC, multi-agent work, Rein and Orca, then apply the "
        "relevant playbook to the current request; preserve existing approval and workspace boundaries. "
        "This mode does not authorize extra work, recursive delegation, external writes or an unattended loop. "
        "Turn off with /crux:crux-mode off or /crux-off."
    )


def _sync(session_id: str, prompt: Any) -> Optional[bool]:
    """Apply an explicit activation command. Returns the new state, or None when unchanged."""
    path = _state_path(session_id)
    if path is None:
        return None
    change = _activation(prompt)
    if change is None:
        return None
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    temporary = path.with_suffix(".tmp")
    temporary.write_text(json.dumps({"active": change}), encoding="utf-8")
    os.replace(temporary, path)
    os.chmod(path, 0o600)
    return change


def _hook_pre_llm_call(**kwargs: Any) -> Optional[Dict[str, Any]]:
    session_id = kwargs.get("session_id")
    if not isinstance(session_id, str) or not session_id.strip():
        return None
    state = _sync(session_id, kwargs.get("user_message"))
    if state is None:
        path = _state_path(session_id)
        state = _read_active(path) if path is not None else False
    if not state:
        return None
    if kwargs.get("is_first_turn"):
        return {"context": _mode_context()}
    # Every turn, so a mode switched off mid-session stops guiding the next one.
    return {"context": _mode_context()}


def register(ctx: Any) -> None:
    """Hermes plugin entry point."""
    ctx.register_hook("pre_llm_call", _hook_pre_llm_call)
