//go:build !linux

package driver

import "strconv"

// KeepDescendants does nothing where the system offers no reaper.
func KeepDescendants() {}

func descendants(int) []int { return nil }

func commandOf(pid int) string { return "pid " + strconv.Itoa(pid) }

func isRunning(int) bool { return true }
