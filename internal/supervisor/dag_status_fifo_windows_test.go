package supervisor

import "testing"

func makeDAGStatusFIFO(t *testing.T, path string) func() {
	t.Helper()
	t.Skip("syscall.Mkfifo is unavailable on Windows")
	return func() {}
}
