package msg

import (
	"errors"
	"fmt"
	"testing"
)

func TestText(t *testing.T) {
	m := T("已经有叫 {name} 的节点了，最多 {n} 个", "name", "dev", "n", 3)
	if m.String() != "已经有叫 dev 的节点了，最多 3 个" || m.Params["n"] != "3" {
		t.Fatalf("got %q %v", m.String(), m.Params)
	}
	if T("没有找到").Params != nil {
		t.Fatal("no params expected")
	}
	err := fmt.Errorf("wrap: %w", Err("仓库 {repo} 不存在", "repo", "o/r"))
	if got := Of(err); got.Key != "仓库 {repo} 不存在" || got.Params["repo"] != "o/r" {
		t.Fatalf("Of = %+v", got)
	}
	if got := Of(errors.New("boom")); got.Key != "boom" || got.Params != nil {
		t.Fatalf("plain = %+v", got)
	}
}
