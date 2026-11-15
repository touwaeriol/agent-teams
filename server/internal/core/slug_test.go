package core

import "testing"

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{"Login page": "login-page", "员工通信与审批": "work", "  Fix #12: CI!! ": "fix-12-ci",
		"A very long title that goes on and on beyond limits": "a-very-long-title-that-goes-on-a"} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q want %q", in, got, want)
		}
	}
}
