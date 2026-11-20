package sshgw

import "testing"

func TestRedact(t *testing.T) {
	cases := []struct{ in, want string }{
		// node/src/util/redact.ts rules
		{"echo ghp_abcdefghijkl", "echo ***"},
		{"curl -H 'Authorization: Bearer abcdefgh12345'", "curl -H 'Authorization: Bearer ***'"},
		{"OPENAI=sk-abcdefghijk run", "OPENAI=*** run"},
		{"x github_pat_ABCDEFGH1234", "x ***"},
		// SSH command lines
		{"mysql -uroot -pS3cretPw db", "mysql -uroot -p*** db"},
		{"mysql -u root -p db", "mysql -u root -p db"},
		{"psql postgres://app:hunter22@db:5432/x", "psql postgres://app:***@db:5432/x"},
		{"tool --password=abc123 --user u", "tool --password=*** --user u"},
		{"tool --token abc123def", "tool --token ***"},
		{`PASSWORD="a b c" ./deploy.sh`, "PASSWORD=*** ./deploy.sh"},
		{"MYSQL_PWD=xyz123 mysql", "MYSQL_PWD=*** mysql"},
		{"export AWS_SECRET_ACCESS_KEY=abcd/efgh", "export AWS_SECRET_ACCESS_KEY=***"},
		{"sshpass -p hunter2 ssh x", "sshpass -p *** ssh x"},
		{"docker compose up -d", "docker compose up -d"},
		{"tar -pxf a.tgz && ls -pla", "tar -pxf a.tgz && ls -pla"},
		{"mysqldump -h db -pS3cret x | gzip", "mysqldump -h db -p*** x | gzip"},
		{"ls -la /srv && echo done", "ls -la /srv && echo done"},
	}
	for _, c := range cases {
		if got := Redact(c.in); got != c.want {
			t.Errorf("Redact(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// The server's own secrets are masked by value wherever they appear; tiny values are ignored.
	got := Redact("echo 'Tr0ub4dor&3' | sudo -S systemctl restart app; echo pp-phrase-9", "Tr0ub4dor&3", "pp-phrase-9", "a")
	if got != "echo '***' | sudo -S systemctl restart app; echo ***" {
		t.Errorf("by value: %q", got)
	}
}
