//go:build !darwin && !linux

package supervisor

import "os"

// Other platforms use COLUMNS or the 80-column fallback.
func dagTerminalColumns(*os.File) int { return 0 }
