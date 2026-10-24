//go:build !windows

package playback

import "os/exec"

func configureCommand(*exec.Cmd) {}
