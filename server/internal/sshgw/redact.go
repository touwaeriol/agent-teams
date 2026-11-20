package sshgw

import (
	"regexp"
	"sort"
	"strings"
)

// Redaction of commands before they reach at_ssh_audit or at_events (remote-servers.md §9.4): the
// rules of node/src/util/redact.ts (GitHub tokens, sk- keys, Bearer …) plus the forms secrets take
// on SSH command lines, then the server's own password and key passphrase replaced by value.

const mask = "***"

type rule struct {
	re   *regexp.Regexp
	keep int // capture groups kept before the secret (1 = keep group 1, mask the rest)
}

var rules = []rule{
	{re: regexp.MustCompile(`gh[pousr]_[A-Za-z0-9_]{8,}`)},
	{re: regexp.MustCompile(`github_pat_[A-Za-z0-9_]{8,}`)},
	{re: regexp.MustCompile(`sk-[A-Za-z0-9_-]{8,}`)},
	{re: regexp.MustCompile(`(?i)(Bearer\s+)[A-Za-z0-9._~+/=-]{8,}`), keep: 1},
	// URL credentials: scheme://user:pass@host
	{re: regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://[^\s:/@]+:)[^\s@/]+(@)`), keep: 2},
	// --password=x, --passwd x, --token=x, --secret x, --api-key=x (quoted or not)
	{re: regexp.MustCompile(`(?i)(--?(?:password|passwd|pass|pwd|token|secret|api[-_]?key|access[-_]?key)(?:=|\s+))("[^"]*"|'[^']*'|\S+)`), keep: 1},
	// PASSWORD=x, MYSQL_PWD=x, DB_PASS=x, AWS_SECRET_ACCESS_KEY=x, GITHUB_TOKEN=x
	{re: regexp.MustCompile(`(?i)(\b[A-Z0-9_]*(?:PASSWORD|PASSWD|PASS|PWD|SECRET|TOKEN|API_?KEY|ACCESS_KEY)[A-Z0-9_]*=)("[^"]*"|'[^']*'|\S+)`), keep: 1},
	// mysql -pSECRET (the password glued to -p); a lone -p (prompt) stays, other tools' -p flags too
	{re: regexp.MustCompile(`(\b(?:mysql|mysqldump|mysqladmin|mariadb|mariadb-dump)\b[^|;&]*?\s-p)([^\s-]\S*)`), keep: 1},
	// sshpass -p SECRET
	{re: regexp.MustCompile(`(\bsshpass\s+-p\s*)(\S+)`), keep: 1},
}

// Redact masks secrets in a command line. values are secrets known by value (the server's password
// and key passphrase): every occurrence is replaced, longest first, before the patterns run.
func Redact(text string, values ...string) string {
	vals := make([]string, 0, len(values))
	for _, v := range values {
		if len(v) >= 3 { // a one- or two-character "secret" would mask half the command
			vals = append(vals, v)
		}
	}
	sort.Slice(vals, func(i, j int) bool { return len(vals[i]) > len(vals[j]) })
	for _, v := range vals {
		text = strings.ReplaceAll(text, v, mask)
	}
	for _, r := range rules {
		text = r.re.ReplaceAllStringFunc(text, func(m string) string {
			if r.keep == 0 {
				return mask
			}
			sub := r.re.FindStringSubmatch(m)
			switch r.keep {
			case 1:
				return sub[1] + mask
			default: // URL: keep scheme://user: and the @
				return sub[1] + mask + sub[2]
			}
		})
	}
	return text
}
