package nodegw

import (
	"testing"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"
)

func TestIdemKeyMatchesDaemon(t *testing.T) {
	cases := map[string][2]string{
		protocol.TypeSessionSend:      {`{"session_id":"s_1","msg_id":"m_9","text":"x"}`, "session.send:m_9"},
		protocol.TypeSessionOpen:      {`{"session_id":"s_1","open_gen":3}`, "session.open:s_1:3"},
		protocol.TypeApprovalResolve:  {`{"approval_id":"ap_1","decision":"allow","decided_by":"user:u"}`, "approval.resolve:ap_1"},
		protocol.TypeSessionInterrupt: {`{"session_id":"s_1"}`, "session.interrupt:c_env"},
		protocol.TypeGitPush:          {`{"job_id":"g_7"}`, "git.push:g_7"},
	}
	for typ, c := range cases {
		got, err := idemKey(typ, "c_env", []byte(c[0]))
		if err != nil || got != c[1] {
			t.Errorf("%s: %q %v want %q", typ, got, err, c[1])
		}
	}
	if _, err := idemKey(protocol.TypeToolCall, "c_x", []byte(`{}`)); err == nil {
		t.Fatal("daemon messages are not console commands")
	}
}

func TestExpiry(t *testing.T) {
	if expiry(protocol.TypeSessionSend) != 10*time.Minute || expiry(protocol.TypeToolResult) != 24*time.Hour ||
		expiry(protocol.TypeGitPrepare) != 30*time.Minute || expiry(protocol.TypeSessionOpen) != 30*time.Minute {
		t.Fatal("expiry table does not follow protocol.md §2.1")
	}
}
