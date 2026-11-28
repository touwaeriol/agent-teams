package workflows

import (
	"fmt"
	"strings"
)

var kindText = map[string]string{
	"work":       "做事",
	"review":     "审查",
	"decision":   "决策",
	approvalKind: "等用户确认",
}

const renderGuide = `这是用户给这个项目配的工作流程（流程图），拆任务、指派岗位时按它来：每个步骤建成交给对应岗位的任务，用 depends_on 体现先后；一个步骤“然后”指向几个步骤时，这几个步骤并行（各自只依赖前一步，不要串起来），一个步骤有几条“然后”指进来时，要等它们全部完成（depends_on 写全）；一个步骤连出几条“条件分支”时只走其中一条：按线上的条件判断走哪条，没走的分支上的步骤不建任务，汇合处只依赖实际走过的那条；审查、决策步骤不通过时，按“不通过退回”的连线给被退回的步骤开返工任务，再重新走后面的步骤；“等用户确认”的步骤用 ask_user 请用户确认后再继续；岗位是组长（lead）的步骤由你自己做。流程是参考：遇到流程没覆盖的情况可以调整或加任务，但要在回复里说明原因。`

// Render is the flow as the lead's system prompt section; titles maps member role keys to
// their titles (a key missing there is a role no longer in the team).
func Render(w Flow, titles map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## 工作流程：%s\n%s\n", w.Name, renderGuide)
	if w.Description != "" {
		b.WriteString("\n流程说明（用户编写）：\n" + w.Description + "\n")
	}
	byID := map[string]FlowStep{}
	for _, s := range w.Steps {
		byID[s.ID] = s
	}
	b.WriteString("\n流程图（Mermaid；实线 = 然后，带条件的实线 = 条件分支，虚线 = 不通过退回）：\n")
	b.WriteString(Mermaid(w, titles))
	b.WriteString("\n\n每一步的详细说明（按先后排列）：\n")
	for i, s := range Ordered(w) {
		fmt.Fprintf(&b, "%d. %s（%s，%s）\n", i+1, s.Name, roleText(s, titles), kindText[s.Kind])
		if s.Instructions != "" {
			b.WriteString("   说明：" + indent(s.Instructions) + "\n")
		}
		if out := outgoing(w, s.ID, byID); out != "" {
			b.WriteString("   " + out + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// Mermaid is the flow as a Mermaid flowchart (left to right), the same chart the user drew.
func Mermaid(w Flow, titles map[string]string) string {
	ids := map[string]string{}
	var b strings.Builder
	b.WriteString("```mermaid\nflowchart LR\n")
	for i, s := range Ordered(w) {
		ids[s.ID] = fmt.Sprintf("s%d", i+1)
		fmt.Fprintf(&b, "  %s[\"%s<br/>%s · %s\"]\n", ids[s.ID], mermaidText(s.Name), mermaidText(shortRole(s, titles)), kindText[s.Kind])
	}
	for _, e := range w.Edges {
		from, to := ids[e.From], ids[e.To]
		switch {
		case e.Kind == rejectEdge:
			fmt.Fprintf(&b, "  %s -.->|\"%s\"| %s\n", from, mermaidText(firstNonEmpty(e.Label, "不通过退回")), to)
		case e.Label != "":
			fmt.Fprintf(&b, "  %s -->|\"%s\"| %s\n", from, mermaidText(e.Label), to)
		default:
			fmt.Fprintf(&b, "  %s --> %s\n", from, to)
		}
	}
	b.WriteString("```")
	return b.String()
}

func shortRole(s FlowStep, titles map[string]string) string {
	switch {
	case s.Role == "":
		return "用户"
	case s.Role == leadRole:
		return "组长"
	}
	if t := titles[s.Role]; t != "" {
		return t
	}
	return s.Role
}

// mermaidText keeps a label inside its quotes and on one line.
func mermaidText(v string) string {
	return strings.NewReplacer(`"`, "#quot;", "\n", " ", "\r", " ", "`", "'").Replace(v)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func roleText(s FlowStep, titles map[string]string) string {
	switch {
	case s.Role == "":
		return "用户"
	case s.Role == leadRole:
		return "组长，你自己"
	}
	if t, ok := titles[s.Role]; ok && t != "" && t != s.Role {
		return "岗位 " + t + "（" + s.Role + "）"
	}
	if _, ok := titles[s.Role]; ok {
		return "岗位 " + s.Role
	}
	return "岗位 " + s.Role + "，已不在团队里，换一个合适的岗位"
}

func outgoing(w Flow, from string, byID map[string]FlowStep) string {
	var next, branch, back []string
	for _, e := range w.Edges {
		if e.From != from {
			continue
		}
		name := byID[e.To].Name
		switch {
		case e.Kind == branchEdge:
			branch = append(branch, "如果"+e.Label+" → "+name)
		case e.Label != "":
			name += "（" + e.Label + "）"
		}
		switch e.Kind {
		case rejectEdge:
			back = append(back, name)
		case nextEdge:
			next = append(next, name)
		}
	}
	var parts []string
	if len(next) > 0 {
		parts = append(parts, "然后 → "+strings.Join(next, "、"))
	}
	if len(branch) > 0 {
		parts = append(parts, "按条件只走一条："+strings.Join(branch, "；"))
	}
	if len(back) > 0 {
		parts = append(parts, "不通过退回 → "+strings.Join(back, "、"))
	}
	return strings.Join(parts, "；")
}

func indent(text string) string { return strings.ReplaceAll(text, "\n", "\n   ") }

// Ordered lists the steps in flow order: Kahn's algorithm over the next and branch edges, ties (and steps
// on a cycle) in the order they were saved.
func Ordered(w Flow) []FlowStep {
	in := map[string]int{}
	for _, e := range w.Edges {
		if forward(e) {
			in[e.To]++
		}
	}
	done := map[string]bool{}
	out := make([]FlowStep, 0, len(w.Steps))
	for len(out) < len(w.Steps) {
		s, ok := nextReady(w.Steps, in, done)
		if !ok {
			s = firstLeft(w.Steps, done) // a cycle: take the earliest saved step left
		}
		done[s.ID] = true
		out = append(out, s)
		for _, e := range w.Edges {
			if forward(e) && e.From == s.ID {
				in[e.To]--
			}
		}
	}
	return out
}

func nextReady(steps []FlowStep, in map[string]int, done map[string]bool) (FlowStep, bool) {
	for _, s := range steps {
		if !done[s.ID] && in[s.ID] <= 0 {
			return s, true
		}
	}
	return FlowStep{}, false
}

func firstLeft(steps []FlowStep, done map[string]bool) FlowStep {
	for _, s := range steps {
		if !done[s.ID] {
			return s
		}
	}
	return FlowStep{}
}

func forward(e FlowEdge) bool { return e.Kind == nextEdge || e.Kind == branchEdge }
