//go:build windows

package coding

import (
	"errors"
	"math"
	"testing"

	"golang.org/x/sys/windows"
)

func loaderProcessAlive(t *testing.T, pid int) bool {
	t.Helper()
	id := int64(pid)
	if id <= 0 || id > math.MaxUint32 {
		t.Fatalf("invalid process ID %d", pid)
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(id))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		// The positive PID no longer identifies a process.
		return false
	}
	if err != nil {
		t.Fatalf("open process %d: %v", pid, err)
	}
	defer func() {
		if err := windows.CloseHandle(handle); err != nil {
			t.Errorf("close process %d handle: %v", pid, err)
		}
	}()
	event, err := windows.WaitForSingleObject(handle, 0)
	if err != nil {
		t.Fatalf("probe process %d: %v", pid, err)
	}
	switch event {
	case windows.WAIT_OBJECT_0:
		return false
	case uint32(windows.WAIT_TIMEOUT):
		return true
	default:
		t.Fatalf("probe process %d: unexpected wait status %#x", pid, event)
		return false
	}
}
