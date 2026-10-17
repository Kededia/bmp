//go:build !windows

package spectrum

import "os/exec"

func configureCommand(*exec.Cmd) {}
