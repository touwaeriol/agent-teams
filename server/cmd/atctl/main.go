// Command atctl is the agent-teams v2 control plane: serve | migrate |
// healthcheck | openapi | admin.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/touwaeriol/agent-teams/server/internal/gitremote"
)

const usage = `usage: atctl <command> [args]

commands:
  serve                         run the control plane (needs DATABASE_URL, AGENT_TEAMS_PUBLIC_URL)
  migrate [up|status|version]   apply or inspect database migrations (default: up)
  healthcheck                   exit 0 when http://127.0.0.1:$PORT/healthz answers 200
  openapi                       print the OpenAPI 3.1 document (JSON)
  admin reset-password EMAIL [PASSWORD]   set a password (random temporary one if omitted)
  admin set-role EMAIL admin|user         change a user's role (Casbin grouping rule)
  admin reseal                  re-encrypt sealed secrets (and re-wrap remote server credentials) with the first AGENT_TEAMS_SECRET_KEYS key
`

var commands = map[string]func(ctx context.Context, args []string) error{
	"serve":       serve,
	"migrate":     migrate,
	"healthcheck": healthcheck,
	"openapi":     openapi,
	"admin":       admin,
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if os.Args[1] == "git-ssh" { // git's ssh for upstream commands of standalone repositories (internal)
		os.Exit(gitremote.RunHelper(os.Args[2:]))
	}
	run, ok := commands[os.Args[1]]
	if !ok {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "atctl:", err)
		os.Exit(1)
	}
}
