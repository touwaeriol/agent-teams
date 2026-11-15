package office

import (
	"encoding/json"
	"fmt"
)

// byID lists the top-level arrays merged by element id (office-layout.md §3).
var byID = map[string]bool{"rooms": true, "furniture": true, "widgets": true}

// resolve applies "extends": the base is resolved first, then the child is
// deep-merged over it. lookup returns a raw layout by id.
func resolve(raw []byte, lookup func(id string) ([]byte, bool)) ([]byte, error) {
	return resolveDepth(raw, lookup, map[string]bool{})
}

func resolveDepth(raw []byte, lookup func(string) ([]byte, bool), seen map[string]bool) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("不是合法的 JSON：%w", err)
	}
	base, _ := doc["extends"].(string)
	if base == "" {
		return raw, nil
	}
	if seen[base] {
		return nil, fmt.Errorf("extends 循环引用：%s", base)
	}
	seen[base] = true
	baseRaw, ok := lookup(base)
	if !ok {
		return nil, fmt.Errorf("extends 指向不存在的布局：%s", base)
	}
	resolvedBase, err := resolveDepth(baseRaw, lookup, seen)
	if err != nil {
		return nil, err
	}
	var parent map[string]any
	if err := json.Unmarshal(resolvedBase, &parent); err != nil {
		return nil, err
	}
	merged := mergeTop(parent, doc)
	delete(merged, "extends")
	return json.Marshal(merged)
}

func mergeTop(base, over map[string]any) map[string]any {
	out := copyMap(base)
	for k, v := range over {
		if byID[k] {
			out[k] = mergeByID(asSlice(base[k]), asSlice(v))
			continue
		}
		out[k] = mergeValue(base[k], v)
	}
	return out
}

func mergeValue(base, over any) any {
	bm, ok1 := base.(map[string]any)
	om, ok2 := over.(map[string]any)
	if !ok1 || !ok2 {
		return over // scalars and arrays are replaced
	}
	out := copyMap(bm)
	for k, v := range om {
		out[k] = mergeValue(bm[k], v)
	}
	return out
}

func mergeByID(base, over []any) []any {
	out := append([]any(nil), base...)
	index := map[string]int{}
	for i, item := range out {
		index[idOf(item)] = i
	}
	for _, item := range over {
		if i, ok := index[idOf(item)]; ok && idOf(item) != "" {
			out[i] = item
			continue
		}
		out = append(out, item)
	}
	return out
}

func idOf(item any) string {
	m, _ := item.(map[string]any)
	id, _ := m["id"].(string)
	return id
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
