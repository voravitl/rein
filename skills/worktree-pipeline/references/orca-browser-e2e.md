# Orca Browser E2E Testing Guide for Agents

## 1. Overview
In multi-agent pipelines where workers build web applications, SPAs, dashboards, or HTTP services, unit tests alone cannot prove that the user interface actually renders and behaves correctly.

Traditional E2E solutions (Playwright, Cypress, Puppeteer) introduce massive overhead:
- Downloading ~400MB browser binaries.
- Fragile headless browser environments and sandbox permissions.
- Inability for human coordinators to see what the agent is seeing.

**Orca Browser** solves this natively:
- **Built directly into the Orca runtime** (`orca tab`, `orca goto`, `orca snapshot`, `orca click`, `orca fill`, `orca eval`, `orca screenshot`).
- **Interactive Accessibility Tree**: Produces structured text trees with interactive element refs (`@e1`, `@e2`), enabling LLMs to understand the page without heavy vision models.
- **Visual Evidence**: Captures full viewport PNG screenshots for PR review and contract audits.
- **Zero Extra Dependencies**: Handled via `rein e2e` or direct `orca` CLI.

---

## 2. The `rein e2e` CLI Runner

`rein` provides a deterministic E2E runner that executes declarative test specs against Orca Browser:

```sh
# Verify Orca runtime availability
rein e2e check [--json]

# Run an E2E test scenario
rein e2e run <spec.json|spec.md> [--out <evidence-dir>] [--base-url <url>] [--timeout <sec>] [--json]
```

### Exit Codes:
- `0`: PASS (all steps executed successfully)
- `1`: FAIL (assertion failed or step timed out; failure screenshot & snapshot captured)
- `2`: ERROR (invalid spec, missing arguments, or Orca runtime unreachable)

---

## 3. Spec Schema

An E2E spec is a JSON document (or embedded in markdown via ````json ... ````).

```json
{
  "name": "User Registration and Dashboard Flow",
  "base_url": "http://localhost:3000",
  "timeout_seconds": 30,
  "steps": [
    {
      "action": "goto",
      "url": "/login",
      "description": "Open login page"
    },
    {
      "action": "assert_title",
      "value": "Login",
      "description": "Check page title"
    },
    {
      "action": "assert_text",
      "value": "Please sign in",
      "description": "Verify login prompt"
    },
    {
      "action": "fill",
      "target": "Email address",
      "value": "user@example.com",
      "description": "Fill email input"
    },
    {
      "action": "fill",
      "target": "Password",
      "value": "secret123",
      "description": "Fill password input"
    },
    {
      "action": "click",
      "target": "Sign In",
      "description": "Submit login form"
    },
    {
      "action": "wait",
      "value": "Welcome to Dashboard",
      "timeout_ms": 5000,
      "description": "Wait for dashboard landing"
    },
    {
      "action": "eval",
      "expression": "window.location.pathname",
      "assert_value": "/dashboard",
      "description": "Verify redirect"
    },
    {
      "action": "screenshot",
      "filename": "dashboard-verified.png",
      "description": "Save visual proof"
    }
  ]
}
```

### Supported Step Actions:

| Action | Parameters | Description |
|---|---|---|
| `goto` | `url` | Navigates the tab to the URL (appends `base_url` if relative). Automatically refreshes accessibility tree refs. |
| `snapshot` | - | Forces a fresh accessibility tree snapshot. |
| `assert_text` | `value` | Verifies that `value` is present in the current accessibility snapshot. |
| `assert_title` | `value` | Verifies that `document.title` contains `value`. |
| `click` | `target` | Clicks an element. `target` can be an element ref (`@e1`, `e1`) or a fuzzy text name (`"Submit"`, `"Learn more"`). |
| `fill` | `target`, `value` | Clears and types `value` into the input element referenced by `target`. |
| `keypress` | `key` / `value` | Presses a key (e.g. `Enter`, `Tab`, `Escape`). |
| `eval` | `expression`, `assert_value` (opt) | Evaluates JavaScript expression in page context. |
| `wait` | `value` (text to poll), `timeout_ms` | Waits until text appears, or sleeps for `timeout_ms`. |
| `screenshot` | `filename` | Captures viewport screenshot and saves PNG into evidence directory. |

---

## 4. Resilient Element Targeting

Unlike brittle CSS/XPath selectors that break on class name changes, Orca uses an **Accessibility Ref Map**:

1. **Direct Ref**: `@e1` or `e1` directly clicks/fills the corresponding element from `orca snapshot`.
2. **Text / Label Resolution**: Passing `"Submit"` or `"Username"` tells `rein e2e` to scan the accessibility tree:
   - Exact match against `ref.name`
   - Case-insensitive match
   - Substring match
3. When the element is clicked or navigated, `rein e2e` automatically re-snapshots so element refs stay in sync.

---

## 5. Diagnostic Evidence on Failure

If any step fails, `rein e2e`:
1. Immediately captures `failure-step-<index>.png` (full viewport image).
2. Captures `failure-step-<index>.txt` (full accessibility snapshot at the point of failure).
3. Generates `report.json` and `report.md` detailing which step failed and why.
4. Guaranteed cleanup: closes the browser tab via `defer` so tabs never leak.

---

## 6. Contract & Drift Integration

To satisfy `rein contract`:
1. The contract marks `e2e` in scope and specifies a report path:
   ```sh
   rein contract new --name task-1 --scope e2e:dashboard --report-path report/task-1.md
   ```
2. The agent executes the test and outputs evidence to `report/e2e`:
   ```sh
   rein e2e run tests/e2e/dashboard.json --out report/e2e
   ```
3. The generated `report/e2e/report.md` and screenshots (`report/e2e/*.png`) are referenced in `report/task-1.md` as concrete test evidence.
4. `rein drift task-1` verifies the evidence before allowing merge.
