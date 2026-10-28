package supervisor

import (
	"fmt"
	"io"
	"os"
)

// readDAGStatusFile rejects special files before opening and verifies that the
// opened descriptor still names the checked regular file. Unix opens also use
// nonblocking/no-follow flags to cover replacement between Lstat and open.
func readDAGStatusFile(path string, maxBytes int64) ([]byte, error) {
	checked, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !checked.Mode().IsRegular() {
		return nil, fmt.Errorf("status file %s is not a regular file", path)
	}
	if maxBytes > 0 && checked.Size() > maxBytes {
		return nil, fmt.Errorf("status file %s exceeds %d bytes", path, maxBytes)
	}
	f, err := openDAGStatusFile(path)
	if err != nil {
		return nil, fmt.Errorf("open checked status file %s: %v", path, err)
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(checked, opened) {
		return nil, fmt.Errorf("status file %s changed during open", path)
	}
	current, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("recheck status file %s: %v", path, err)
	}
	if !current.Mode().IsRegular() || !os.SameFile(opened, current) {
		return nil, fmt.Errorf("status file %s changed during open", path)
	}
	if maxBytes > 0 && opened.Size() > maxBytes {
		return nil, fmt.Errorf("status file %s exceeds %d bytes", path, maxBytes)
	}
	// Journal reads stop at the observed size even if a controller keeps
	// appending. Telemetry reads allow one extra byte to detect concurrent growth.
	limit := opened.Size()
	if maxBytes > 0 {
		limit = maxBytes + 1
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit))
	if err != nil {
		return nil, err
	}
	if maxBytes > 0 && int64(len(raw)) > maxBytes {
		return nil, fmt.Errorf("status file %s exceeds %d bytes", path, maxBytes)
	}
	return raw, nil
}
