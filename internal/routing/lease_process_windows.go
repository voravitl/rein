package routing

import "errors"

func dedicatedLeaseGroup(pid int) error {
	return errors.New("local provider process group proof is unsupported on Windows")
}

func leaseGroupGone(group int) error {
	return errors.New("local provider process group proof is unsupported on Windows")
}

func awaitLeaseGroupGone(group int) error {
	return errors.New("local provider process group proof is unsupported on Windows")
}
