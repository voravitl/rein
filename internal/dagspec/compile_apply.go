package dagspec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/routing"
	"github.com/voravitl/rein/internal/supervisor"
)

// Apply materializes detached worktrees and contracts for each task, validates
// the plan in-process against supervisor rules, and refuses any tampered existing state.
func (c *Compiled) Apply() error {
	for _, t := range c.Plan.Tasks {
		if t.Kind != "worker" {
			continue
		}

		ct, ok := c.Contracts[t.Task]
		if !ok {
			return fmt.Errorf("contract for task %s not found in compiled state", t.Task)
		}

		// Ensure worktree exists
		if _, err := os.Stat(ct.Worktree); os.IsNotExist(err) {
			if err := os.MkdirAll(filepath.Dir(ct.Worktree), 0755); err != nil {
				return fmt.Errorf("create worktree parent dir: %w", err)
			}
			cmd := exec.Command("git", "-C", c.Plan.Repo, "worktree", "add", "--detach", ct.Worktree, t.Base)
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("git worktree add for %s: %s: %w", t.Task, string(out), err)
			}
		} else if err != nil {
			return fmt.Errorf("stat worktree %s: %w", ct.Worktree, err)
		}

		// Check existing contract if present for tampering
		contractPath := contract.PathOf(ct.Name)
		ctBytes, err := json.MarshalIndent(ct, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal contract for %s: %w", t.Task, err)
		}

		if existing, err := os.ReadFile(contractPath); err == nil {
			var existingCt contract.Contract
			if err := json.Unmarshal(existing, &existingCt); err != nil {
				return fmt.Errorf("refusing corrupted existing contract %s: %w", contractPath, err)
			}
			if routing.ContractHash(&existingCt) != t.ContractHash {
				return fmt.Errorf("refusing tampered contract %s (contract hash mismatch)", contractPath)
			}
			if !bytes.Equal(bytes.TrimSpace(existing), bytes.TrimSpace(ctBytes)) {
				return fmt.Errorf("refusing tampered contract %s (content differs)", contractPath)
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("read contract %s: %w", contractPath, err)
		}

		// Save contract
		if _, err := ct.Save(); err != nil {
			return fmt.Errorf("save contract for %s: %w", t.Task, err)
		}
	}

	// Validate compiled plan in-process using supervisor validation
	if _, _, err := supervisor.Decode(c.PlanJSON); err != nil {
		return fmt.Errorf("supervisor validation failed: %w", err)
	}

	// Write plan.json and plan.md to output directory
	if err := c.WriteOutputs(); err != nil {
		return err
	}

	return nil
}

// ValidateInProcess runs the supervisor plan validator on the compiled plan.
func (c *Compiled) ValidateInProcess() error {
	if c == nil || len(c.PlanJSON) == 0 {
		return errors.New("nil or empty compiled plan")
	}
	_, _, err := supervisor.Decode(c.PlanJSON)
	return err
}
