package supervisor

import "os"

func openDAGStatusFile(path string) (*os.File, error) {
	// The caller checks regular-file type before open and descriptor identity
	// afterwards; Unix nonblocking/no-follow flags are unavailable here.
	return os.Open(path)
}
