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
	err := Err("仓库 {repo} 不存在", "repo", "o/r")
	if got := Of(err); got.Key != "仓库 {repo} 不存在" || got.Params["repo"] != "o/r" {
		t.Fatalf("Of = %+v", got)
	}
	// More context around a message: the whole text, untranslated, rather than losing the context.
	if got := Of(fmt.Errorf("任务 T-1：%w", err)); got.Key != "任务 T-1：仓库 o/r 不存在" {
		t.Fatalf("Of(wrapped) = %+v", got)
	}
	nested := T("{host} 解析到 {why}", "host", "h", "why", T("回环地址"))
	if nested.String() != "h 解析到 回环地址" || nested.Nested["why"].Key != "回环地址" {
		t.Fatalf("nested = %+v", nested)
	}
	base := Err("连接超时")
	w := Wrap(fmt.Errorf("x: %w", base), "连接 {addr} 超时", "addr", "h:22")
	if !errors.Is(w, base) || Of(w).String() != "连接 h:22 超时" {
		t.Fatalf("wrap = %v %+v", errors.Is(w, base), Of(w))
	}
	if got := Of(errors.New("boom")); got.Key != "boom" || got.Params != nil {
		t.Fatalf("plain = %+v", got)
	}
}
