//go:build !windows

package app

func terminalHasSixel() bool {
	return false
}
