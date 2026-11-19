package github

import (
	"bytes"
	"log/slog"
	"runtime"
	"strings"
)

// A GitHub call made while the calling goroutine is inside a database transaction keeps the
// transaction's connection and row locks for the whole round trip (up to the client timeout),
// and every writer of those rows queues behind it. Network calls belong before the transaction
// or in an AfterCommit callback; this guard reports any that slip through.

// txFrame is the frame every transaction (core.InTx, store.Tx, savepoints) runs under.
const txFrame = "gorm.io/gorm.(*DB).Transaction("

// InTxCall is called for a GitHub request issued inside a transaction (tests replace it to fail).
var InTxCall = func(method, path string, stack []byte) {
	slog.Warn("github call inside a database transaction", "method", method, "path", path,
		"stack", OwnFrames(stack))
}

func guardTx(method, path string) {
	buf := make([]byte, 256<<10)
	buf = buf[:runtime.Stack(buf, false)]
	if bytes.Contains(buf, []byte(txFrame)) {
		InTxCall(method, path, buf)
	}
}

// OwnFrames keeps the agent-teams function names of a goroutine stack (innermost first).
func OwnFrames(stack []byte) string {
	var out []string
	for _, line := range strings.Split(string(stack), "\n") {
		if strings.HasPrefix(line, "github.com/touwaeriol/agent-teams/") {
			name := strings.TrimPrefix(line, "github.com/touwaeriol/agent-teams/server/internal/")
			if i := strings.LastIndex(name, "("); i > 0 {
				name = name[:i]
			}
			out = append(out, name)
		}
	}
	return strings.Join(out, " < ")
}
