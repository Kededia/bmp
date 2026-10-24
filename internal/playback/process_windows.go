//go:build windows

package playback

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// configureCommand hides ffmpeg's console window and runs it above normal
// priority, so programs starting up do not starve the decoder.
func configureCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.ABOVE_NORMAL_PRIORITY_CLASS}
}

// raisePriority runs BMP above normal priority for the same reason: the audio
// output thread lives in this process. Child processes such as yt-dlp still
// start at normal priority.
func raisePriority() {
	_ = windows.SetPriorityClass(windows.CurrentProcess(), windows.ABOVE_NORMAL_PRIORITY_CLASS)
}
