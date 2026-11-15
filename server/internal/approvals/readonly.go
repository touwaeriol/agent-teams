package approvals

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"

	"github.com/touwaeriol/agent-teams/server/internal/core"
)

// DecidedReadOnly marks approvals the read-only allow list let through.
const DecidedReadOnly = "policy:rule:builtin-read-only"

// The read-only allow list (architecture.md §9 ②½): read_only and reviewer roles get shell
// commands that only read the worktree without a card. Anything with shell operators,
// absolute or parent paths, or a flag that writes or runs programs still asks.
var (
	sedPrint    = regexp.MustCompile(`^(\d+|\$)(,(\d+|\$))?p$`)
	readCommand = map[string]func(args []string) bool{
		"ls": plainArgs, "cat": plainArgs, "head": plainArgs, "tail": noFollow, "wc": plainArgs, "pwd": plainArgs,
		"grep": plainArgs, "rg": rgArgs, "find": findArgs, "sed": sedArgs, "git": gitArgs,
	}
	findDeny = []string{"-exec", "-execdir", "-ok", "-okdir", "-delete", "-fprint", "-fprint0", "-fprintf", "-fls"}
	rgDeny   = []string{"--pre", "--pre-glob", "-z", "--search-zip"}
	gitRead  = []string{"log", "status", "diff", "show", "rev-parse", "ls-files", "blame", "merge-base"}
	gitDeny  = []string{"--output", "-o", "--ext-diff", "--textconv", "--exec-path", "--git-dir", "--work-tree"}
	listFlag = []string{"--list", "-l", "-a", "--all", "-r", "--remotes", "-v", "-vv", "--verbose", "--no-color", "--color",
		"--show-current"}
)

// readOnlyRole reports whether the employee is a read_only or reviewer role (or the lead).
func readOnlyRole(e core.Employee) bool {
	return e.Tier == "lead" || readOnly(e) || e.Spec.Gate == "review" || e.Spec.Gate == "decision"
}

// ReadOnlyCommand reports whether a shell tool call only reads files inside the worktree.
func ReadOnlyCommand(tool string, input json.RawMessage) bool {
	if tool != "Bash" && tool != "shell" {
		return false
	}
	var in struct {
		Command any `json:"command"`
	}
	if json.Unmarshal(input, &in) != nil {
		return false
	}
	cmd, _ := in.Command.(string)
	argv, ok := splitWords(cmd)
	if !ok || len(argv) == 0 {
		return false
	}
	check, ok := readCommand[argv[0]]
	return ok && insideWorktree(argv[1:]) && check(argv[1:])
}

// Characters that make the shell do more than run one command: outside quotes, and the
// ones double quotes still expand.
const (
	bareMeta   = ";&|<>$`(){}\\!#\n\r"
	doubleMeta = "$`\\!"
)

// splitWords splits a command line into words like a POSIX shell would, refusing any
// operator, redirection, expansion or substitution. Single quotes keep everything literal.
func splitWords(cmd string) ([]string, bool) {
	w := words{}
	for _, r := range cmd {
		if !w.add(r) {
			return nil, false
		}
	}
	if w.quote != 0 {
		return nil, false
	}
	w.flush()
	return w.out, true
}

type words struct {
	out   []string
	cur   strings.Builder
	quote rune
	has   bool
}

func (w *words) add(r rune) bool {
	switch {
	case w.quote != 0:
		return w.quoted(r)
	case r == '\'' || r == '"':
		w.quote, w.has = r, true
	case r == ' ' || r == '\t':
		w.flush()
	case strings.ContainsRune(bareMeta, r):
		return false
	default:
		w.cur.WriteRune(r)
	}
	return true
}

func (w *words) quoted(r rune) bool {
	if w.quote == '"' && strings.ContainsRune(doubleMeta, r) {
		return false
	}
	if r == w.quote {
		w.quote = 0
		return true
	}
	w.cur.WriteRune(r)
	return true
}

func (w *words) flush() {
	if w.has || w.cur.Len() > 0 {
		w.out = append(w.out, w.cur.String())
	}
	w.cur.Reset()
	w.has = false
}

// insideWorktree refuses absolute paths, home directories (~) and parent directories anywhere in the arguments.
func insideWorktree(args []string) bool {
	for _, a := range args {
		_, value, _ := strings.Cut(a, "=")
		for _, s := range []string{a, value} {
			if strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~") || s == ".." || strings.HasPrefix(s, "../") || strings.Contains(s, "/../") || strings.HasSuffix(s, "/..") {
				return false
			}
		}
	}
	return true
}

func plainArgs([]string) bool { return true }

func noFollow(args []string) bool {
	return !slices.ContainsFunc(args, func(a string) bool {
		return a == "-f" || a == "-F" || strings.HasPrefix(a, "--follow") || (strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.ContainsAny(a, "fF"))
	})
}

func findArgs(args []string) bool { return !hasFlag(args, findDeny) }

func rgArgs(args []string) bool { return !hasFlag(args, rgDeny) }

// sedArgs allows `sed -n <range>p files…` only.
func sedArgs(args []string) bool {
	if len(args) < 2 || args[0] != "-n" {
		return false
	}
	return sedPrint.MatchString(args[1]) && !slices.ContainsFunc(args[2:], func(a string) bool { return strings.HasPrefix(a, "-") })
}

// gitArgs allows the read subcommands, and `git branch` in its list form.
func gitArgs(args []string) bool {
	if len(args) == 0 || hasFlag(args, gitDeny) {
		return false
	}
	if args[0] == "branch" {
		return listOnly(args[1:])
	}
	return slices.Contains(gitRead, args[0])
}

func listOnly(args []string) bool {
	for _, a := range args {
		if !slices.Contains(listFlag, a) {
			return false
		}
	}
	return true
}

// hasFlag matches a flag exactly, as --flag=value, or as a short flag with its value attached.
func hasFlag(args, flags []string) bool {
	for _, a := range args {
		for _, f := range flags {
			if a == f || strings.HasPrefix(a, f+"=") || (len(f) == 2 && !strings.HasPrefix(a, "--") && strings.HasPrefix(a, f)) {
				return true
			}
		}
	}
	return false
}
