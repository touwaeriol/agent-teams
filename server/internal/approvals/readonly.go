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
	rgDeny   = []string{"--pre", "--pre-glob", "-z", "--search-zip", "--hostname-bin"}
	gitRead  = []string{"log", "status", "diff", "show", "rev-parse", "ls-files", "blame", "merge-base", "describe", "shortlog",
		"show-ref", "for-each-ref", "name-rev", "rev-list", "cat-file", "count-objects"}
	gitDeny = []string{"--output", "-o", "--ext-diff", "--textconv", "--filters", "--exec-path", "--git-dir", "--work-tree",
		"--upload-pack"}
	// git fetch / ls-remote options that only read the remote or update remote-tracking refs.
	fetchFlag = []string{"--prune", "-p", "--prune-tags", "-P", "--all", "--tags", "-t", "--no-tags", "-n", "--quiet", "-q",
		"--verbose", "-v", "--dry-run", "--unshallow", "--heads", "-h", "--refs", "--symref", "--get-url", "--branches", "-b"}
	fetchFlagValue = regexp.MustCompile(`^--(depth|deepen|shallow-since|sort)=[^=]+$`)
	listFlag       = []string{"--list", "-l", "-a", "--all", "-r", "--remotes", "-v", "-vv", "--verbose", "--no-color", "--color",
		"--show-current"}
	// Commands whose options cannot write or run programs, so a glob expanding to a file
	// named like a flag (say "-delete" or "--pre=x") stays harmless.
	globSafe = []string{"ls", "cat", "head", "wc", "grep"}
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
	argv, glob, ok := splitWords(cmd)
	if !ok || len(argv) == 0 || (glob && !slices.Contains(globSafe, argv[0])) {
		return false
	}
	check, ok := readCommand[argv[0]]
	return ok && insideWorktree(argv[1:]) && check(argv[1:])
}

// Characters that make the shell do more than run one command: outside quotes, and the
// ones double quotes still expand.
const (
	bareMeta   = ";&|<>$`(){}[]\\!#\n\r"
	doubleMeta = "$`\\!"
)

// splitWords splits a command line into words like a POSIX shell would, refusing any
// operator, redirection, expansion or substitution. Single quotes keep everything literal.
// glob reports an unquoted * or ?; a glob that could reach a dot entry (.* or .?, which
// match ..) and bracket globs are refused outright.
func splitWords(cmd string) (argv []string, glob, ok bool) {
	w := words{}
	for _, r := range cmd {
		if !w.add(r) {
			return nil, false, false
		}
	}
	if w.quote != 0 {
		return nil, false, false
	}
	w.flush()
	return w.out, w.glob, true
}

type words struct {
	out   []string
	cur   strings.Builder
	quote rune
	has   bool
	glob  bool
	seg   string // the current path segment so far
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
	case r == '*' || r == '?':
		return w.globChar(r)
	default:
		w.write(r)
	}
	return true
}

// globChar takes an unquoted * or ?, refusing it where it could match "." or "..".
func (w *words) globChar(r rune) bool {
	if strings.HasPrefix(w.seg, ".") {
		return false
	}
	w.glob = true
	w.write(r)
	return true
}

func (w *words) write(r rune) {
	w.cur.WriteRune(r)
	w.seg += string(r)
	if r == '/' || r == '=' {
		w.seg = ""
	}
}

func (w *words) quoted(r rune) bool {
	if w.quote == '"' && strings.ContainsRune(doubleMeta, r) {
		return false
	}
	if r == w.quote {
		w.quote = 0
		return true
	}
	w.write(r)
	return true
}

func (w *words) flush() {
	if w.has || w.cur.Len() > 0 {
		w.out = append(w.out, w.cur.String())
	}
	w.cur.Reset()
	w.has, w.seg = false, ""
}

// insideWorktree refuses absolute paths, home directories (~) and parent directories anywhere
// in the arguments, including values attached to a flag (--file=/x, -f/x).
func insideWorktree(args []string) bool {
	for _, a := range args {
		_, value, _ := strings.Cut(a, "=")
		if shortFlag(a) && strings.ContainsAny(a, "/~") {
			return false
		}
		for _, s := range []string{a, value} {
			if strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~") || s == ".." || strings.HasPrefix(s, "../") || strings.Contains(s, "/../") || strings.HasSuffix(s, "/..") {
				return false
			}
		}
	}
	return true
}

func shortFlag(a string) bool { return strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") }

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

// gitArgs allows the read subcommands, and `git branch` in its list form. Leading
// `-C <dir>` options are allowed (insideWorktree has already refused absolute and parent
// paths): sessions work one directory above their repositories and cannot cd.
func gitArgs(args []string) bool {
	args, ok := stripGitDirs(args)
	if !ok || len(args) == 0 || hasFlag(args, gitDeny) {
		return false
	}
	rest := args[1:]
	switch args[0] {
	case "branch", "tag":
		return listOnly(rest)
	case "remote":
		return listOnly(rest) || (len(rest) > 0 && (rest[0] == "get-url" || rest[0] == "show"))
	case "fetch", "ls-remote":
		return fetchOnly(rest)
	case "reflog":
		return len(rest) == 0 || (rest[0] != "expire" && rest[0] != "delete")
	case "stash", "worktree":
		return len(rest) > 0 && rest[0] == "list"
	}
	return slices.Contains(gitRead, args[0])
}

// stripGitDirs drops leading `-C <dir>` / `-C<dir>` options; ok is false for a -C without a directory.
func stripGitDirs(args []string) ([]string, bool) {
	for len(args) > 0 && strings.HasPrefix(args[0], "-C") {
		if args[0] != "-C" {
			args = args[1:]
			continue
		}
		if len(args) < 2 || args[1] == "" {
			return nil, false
		}
		args = args[2:]
	}
	return args, true
}

// fetchOnly allows a configured remote name and plain refs: no URLs, paths or refspecs with a
// destination (a:b, +a), so only FETCH_HEAD and remote-tracking refs change.
func fetchOnly(args []string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			if !slices.Contains(fetchFlag, a) && !fetchFlagValue.MatchString(a) {
				return false
			}
			continue
		}
		if a == "" || strings.Contains(a, ":") || strings.ContainsAny(a[:1], "/.~+") {
			return false
		}
	}
	return true
}

func listOnly(args []string) bool {
	for _, a := range args {
		if !slices.Contains(listFlag, a) {
			return false
		}
	}
	return true
}

// hasFlag matches a flag exactly, as --flag=value, as an abbreviation (git accepts unique
// prefixes of long options), or as a short flag anywhere in a bundle (-nz).
func hasFlag(args, flags []string) bool {
	for _, a := range args {
		for _, f := range flags {
			if matchesFlag(a, f) {
				return true
			}
		}
	}
	return false
}

func matchesFlag(a, f string) bool {
	switch {
	case len(f) == 2:
		return shortFlag(a) && strings.ContainsRune(a[1:], rune(f[1]))
	case !strings.HasPrefix(f, "--"):
		return a == f // find's primaries: no abbreviations
	}
	name, _, _ := strings.Cut(a, "=")
	return len(name) > 3 && strings.HasPrefix(name, "--") && strings.HasPrefix(f, name)
}
