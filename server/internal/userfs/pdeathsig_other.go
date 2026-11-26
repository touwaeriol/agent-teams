//go:build !linux

package userfs

import "os/exec"

// setPdeathsig: only Linux can tie a child's life to its parent; Close stops the gateways elsewhere.
func setPdeathsig(*exec.Cmd) {}
