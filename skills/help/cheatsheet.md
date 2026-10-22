# `rein` Command Cheatsheet

### 1. Contract & Guard
```sh
# New contract
rein contract new --name TASK --run-dir /tmp/runs/run1 --allow 'src/**,tests/**' --scope S1,S2

# Show contract details
rein contract show TASK

# Install hooks into worktree
rein hooks install TASK --vendors codex,agy,kiro,opencode,hermes,claude

# Verify drift
rein drift TASK --expect-guard kiro --claimed-files src/main.go
```

### 2. Orca Orchestration
```sh
# Call Orca action (deterministic JSON in/out)
rein orca call status
rein orca call '{"action": "status"}'
rein orca schema

# Launch worker
rein orca launch my-task main specs/my-task.md kiro --model claude-sonnet-5.5 --effort high

# Wait for worker signal
rein orca wait --terminal term_coord --timeout-ms 600000

# Drain and ACK messages
rein orca drain --terminal term_coord

# Interactive terminal control
rein orca call send --terminal term_123 --text "go test ./..." --enter
rein orca call read --terminal term_123 --limit 50

# Close worker worktree safely
rein orca close --worktree ~/orca/workspaces/rein/my-task --terminal term_123 --dispatch disp_1

# Audit & clean idle shells
rein orca tidy --apply
```

### 3. Review & Verification
```sh
# Scaffold review report
rein verdict template --mr 42 --sha HEAD --reviewer codex:gpt-6.1-sol --worker kiro:claude-sonnet-5.5

# Record verdict
rein verdict record --mr 42 --sha <SHA> --verdict APPROVE --reviewer codex:gpt-6.1-sol --worker kiro:claude-sonnet-5.5

# Verify merge gate
rein verdict check --mr 42 --sha <SHA> --tier T2

# Human approval prompt
rein approve prompt --mr 42 --sha <SHA>

# Human approval sign-off
rein approve --mr 42 --sha <SHA> --reason "LGTM, all gates passed"
```

### 4. Quota & Providers
```sh
# Probe provider readiness
rein providers --timeout 30s

# Automatic task + model selection
rein route auto --task TASK --run RUN1 --profile-file task-profile.json
```
