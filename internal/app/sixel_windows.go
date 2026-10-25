//go:build windows

package app

import (
	"os"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var readConsoleInput = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReadConsoleInputW")

// inputRecord is a console INPUT_RECORD; key events keep their character at
// byte 10 of the event.
type inputRecord struct {
	eventType uint16
	_         uint16
	event     [16]byte
}

// terminalHasSixel asks the terminal whether it draws Sixel images: its
// reply to the primary device attributes request lists 4 when it does. It
// runs before Bubble Tea reads the console, and gives up after half a second
// for a terminal that does not reply.
func terminalHasSixel() bool {
	input, output := windows.Handle(os.Stdin.Fd()), windows.Handle(os.Stdout.Fd())
	var inputMode, outputMode uint32
	if windows.GetConsoleMode(input, &inputMode) != nil || windows.GetConsoleMode(output, &outputMode) != nil {
		return false
	}
	defer windows.SetConsoleMode(input, inputMode)
	// Terminal replies arrive as typed input only in virtual terminal mode.
	if windows.SetConsoleMode(input, windows.ENABLE_VIRTUAL_TERMINAL_INPUT) != nil ||
		windows.SetConsoleMode(output, outputMode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) != nil {
		return false
	}
	if _, err := os.Stdout.WriteString("\x1b[c"); err != nil {
		return false
	}
	var reply strings.Builder
	records := make([]inputRecord, 64)
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false
		}
		if event, err := windows.WaitForSingleObject(input, uint32(remaining.Milliseconds())+1); err != nil || event != windows.WAIT_OBJECT_0 {
			return false
		}
		var count uint32
		if result, _, _ := readConsoleInput.Call(uintptr(input), uintptr(unsafe.Pointer(&records[0])), uintptr(len(records)), uintptr(unsafe.Pointer(&count))); result == 0 {
			return false
		}
		for _, record := range records[:count] {
			const keyEvent = 1
			if record.eventType == keyEvent && record.event[0] != 0 {
				reply.WriteRune(rune(uint16(record.event[10]) | uint16(record.event[11])<<8))
			}
		}
		text := reply.String()
		if start := strings.Index(text, "\x1b[?"); start >= 0 {
			if end := strings.IndexByte(text[start:], 'c'); end >= 0 {
				for _, attribute := range strings.Split(text[start+3:start+end], ";") {
					if attribute == "4" {
						return true
					}
				}
				return false
			}
		}
	}
}
