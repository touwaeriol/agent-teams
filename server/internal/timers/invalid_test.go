package timers

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/msg"
)

// A schedule validation error carries its message key and params for the web (i18n) and keeps
// its Chinese text for tools and logs.
func TestInvalidCarriesMessage(t *testing.T) {
	_, err := Normalize(Schedule{Name: "n", Message: "m", Kind: "cron", Cron: "0 9 * * *", Timezone: "Mars/Base"}, time.Now(), false, false)
	bad, ok := IsInvalid(fmt.Errorf("wrap: %w", err))
	if !ok || bad.Field != "timezone" {
		t.Fatalf("err = %v", err)
	}
	if bad.Text.Key != "没有这个时区：{tz}" || bad.Text.Params["tz"] != "Mars/Base" || err.Error() != "没有这个时区：Mars/Base" {
		t.Fatalf("text = %+v (%v)", bad.Text, err)
	}
	var m interface{ Msg() msg.Text }
	if !errors.As(err, &m) || m.Msg().Key != bad.Text.Key {
		t.Fatal("ErrInvalid should expose Msg()")
	}
}
