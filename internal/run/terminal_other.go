//go:build !darwin && !linux && !windows

package run

func terminal(fd uintptr) bool { return false }
