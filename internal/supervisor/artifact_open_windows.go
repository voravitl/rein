package supervisor

import (
	"errors"
	"os"
)

func openArtifactFile(root *os.Root, name string) (*os.File, error) {
	return nil, errors.New("verified artifact capture is unsupported on Windows")
}
