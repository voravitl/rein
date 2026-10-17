package hunter

import "os/exec"

// shortcut: Windows cancellation kills only the direct process; descendant
// containment requires a Job Object before Windows reproduction is supported.
func configureReproProcess(cmd *exec.Cmd) {}
func stopReproProcess(cmd *exec.Cmd)      {}

// Directory fsync is unavailable through os.File on Windows.
func syncHunterDirectory(path string) error { return nil }
