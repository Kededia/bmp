//go:build windows

package playback

import (
	"syscall"
	"unsafe"
)

const removeMessage = 0x0001

type messagePoint struct {
	X int32
	Y int32
}

type windowMessage struct {
	Window  uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Point   messagePoint
	Private uint32
}

var (
	user32           = syscall.NewLazyDLL("user32.dll")
	peekMessage      = user32.NewProc("PeekMessageW")
	translateMessage = user32.NewProc("TranslateMessage")
	dispatchMessage  = user32.NewProc("DispatchMessageW")
)

// pumpWindowsMessages lets apartment-threaded COM controls finish asynchronous
// open, codec, and playback transitions without requiring a visible window.
func pumpWindowsMessages() {
	var message windowMessage
	for {
		available, _, _ := peekMessage.Call(
			uintptr(unsafe.Pointer(&message)),
			0,
			0,
			0,
			removeMessage,
		)
		if available == 0 {
			return
		}
		translateMessage.Call(uintptr(unsafe.Pointer(&message)))
		dispatchMessage.Call(uintptr(unsafe.Pointer(&message)))
	}
}
