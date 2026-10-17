//go:build !linux && !darwin

package orca

// The census is only proven on linux and darwin; elsewhere it stays fail-closed, including Windows.
// Remote/SSH hosts stay out of scope by design (same-host policy) and never reach a "gone" verdict.

var _ = ErrCensusUnsupported // keeps the sentinel referenced on all platforms

func censusScan() ([]censusRow, error) { return nil, ErrCensusUnsupported }

func censusMarker() string { return "" }

func censusBoot() string { return "" }
