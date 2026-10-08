## Project rules (<project>)
- **Build and test only with:** `<pack>/gates/backend-test.sh <absolute worktree> <short name> ["filter"]` (throwaway
  database) and `<pack>/gates/frontend-gate.sh <absolute worktree> <short name>`. Never run e2e yourself.
- **Never touch:** <live containers>, the database on <port>, <app ports>, `<owner-only scripts>`.
- **Shared files to keep small:** <files many tasks touch>.
- **Local setup:** <e.g. symlink the main checkout's node_modules; no package install>.
