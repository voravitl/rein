package run

import "os"

// InteractiveStdin checks the actual terminal API, not just the device file mode.
func InteractiveStdin() bool {
	return os.Stdin != nil && terminal(os.Stdin.Fd())
}
