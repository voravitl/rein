---
name: make-bot-ui
description: >-
  Use when building a custom UI (page, dashboard, buttons) that should wake a
  Grok Bot over a webhook, when the user must provide a webhook sender key, or
  when exposing that UI on Tailscale.
---

Read [Runtime guide](../../references/runtime.md) before executing this skill.

# Build a webhook-driven bot UI

The upstream hosted-routine creation, secret-request cards, and automatic wakeups are unavailable in this Codex port. First discover an actual webhook/routine provider tool or use an existing user-specified endpoint. If neither is available, report that precise dependency; do not pretend to create or wake a routine.

When an endpoint is available, build the smallest UI and local server for the requested actions. Keep the webhook URL and credentials server-side, in the project's existing secret store or protected configuration. Do not accept or print secrets in chat. Reuse the provider's documented authentication instead of assuming Cursor-specific headers. Treat incoming JSON as untrusted data, validate its shape, and permit only the named actions. A server-side POST must have a bounded timeout, explicit error reporting, and no retry unless the action is idempotent.

Use the project's existing hosting/network arrangement. Do not install Tailscale, change firewall or bind settings, create scheduler jobs, or expose the app publicly unless requested. Prove one harmless authorized request, the visible result, and the failure path. State whether the real routine woke; an HTTP 200 alone may only prove receipt. Keep screenshots and response evidence free of secrets.
