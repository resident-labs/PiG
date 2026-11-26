package coding

import (
	"os"
	"os/exec"
	"testing"
)

func TestLoaderProcessAlive(t *testing.T) {
	if pid := os.Getpid(); !loaderProcessAlive(t, pid) {
		t.Fatalf("test process %d is not alive", pid)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), executable, "-test.run=^$")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run child test process: %v\n%s", err, output)
	}
	if pid := cmd.Process.Pid; loaderProcessAlive(t, pid) {
		t.Fatalf("child process %d is still alive after Wait", pid)
	}
}
