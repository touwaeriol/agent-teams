package userfs

import (
	"os/exec"
	"syscall"
)

// setPdeathsig has the kernel send the child SIGTERM when the console dies, so a gateway never
// outlives it.
func setPdeathsig(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
}
