//go:build !windows

package coding

import (
	"math"
	"syscall"
	"testing"
)

func loaderProcessAlive(t *testing.T, pid int) bool {
	t.Helper()
	if pid <= 0 || int64(pid) > math.MaxInt32 {
		t.Fatalf("invalid process ID %d", pid)
	}
	switch err := syscall.Kill(pid, 0); err {
	case nil, syscall.EPERM:
		return true
	case syscall.ESRCH:
		return false
	default:
		t.Fatalf("probe process %d: %v", pid, err)
		return false
	}
}
