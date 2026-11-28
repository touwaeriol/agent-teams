package workflows

import (
	"fmt"
	"strings"
)

var kindText = map[string]string{
	"work":       "work",
	"review":     "review",
	"decision":   "decision",
	approvalKind: "user approval",
}

const renderGuide = `Split tasks and assign roles by the flow chart: make each step a task for its role and express the order with depends_on. When a step has several "then" arrows, those steps run in parallel (each depends only on the step before; do not chain them); when several "then" arrows point into a step, it waits for all of them (list them all in depends_on). When a step has several "branch" arrows, take only one: choose by the condition on the line, create no tasks for the steps of the branches not taken, and at the join depend only on the branch actually taken. When a review or decision step fails, follow its "reject" arrow: open a rework task for the step it points to, then run the later steps again. For a "user approval" step, ask the user with ask_user and continue only after they approve. Steps whose role is the lead are yours to do. The flow is a reference: you may adjust it or add tasks for cases it does not cover, but say why in your reply.`

const multiGuide = `This project has %d workflows, each for a different kind of work. Before splitting each task, pick the flow that best fits it (combine several if needed), say in your reply which flow you are following, then split the task by it. Rules inside every flow:`

// RenderAll is the lead's system prompt section for the project's flows: one flow as Render, several
// under one heading with a note to pick the flow that fits each task.
func RenderAll(flows []Flow, titles map[string]string) string {
	if len(flows) == 1 {
		return Render(flows[0], titles)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## Workflows (%d)\n"+multiGuide+" %s\n", len(flows), len(flows), renderGuide)
	for _, w := range flows {
		b.WriteString("\n### Flow: " + w.Name + "\n")
		writeFlow(&b, w, titles)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// Render is the flow as the lead's system prompt section; titles maps member role keys to
// their titles (a key missing there is a role no longer in the team).
func Render(w Flow, titles map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Workflow: %s\n%s\n", w.Name, renderGuide)
	writeFlow(&b, w, titles)
	return strings.TrimRight(b.String(), "\n")
}

func writeFlow(b *strings.Builder, w Flow, titles map[string]string) {
	if w.Description != "" {
		b.WriteString("Flow description (written by the user):\n" + w.Description + "\n")
	}
	byID := map[string]FlowStep{}
	for _, s := range w.Steps {
		byID[s.ID] = s
	}
	b.WriteString("\nFlow chart (Mermaid; solid = then, solid with a condition = branch, dashed = reject):\n")
	b.WriteString(Mermaid(w, titles))
	b.WriteString("\n\nEach step in detail (in flow order):\n")
	for i, s := range Ordered(w) {
		fmt.Fprintf(b, "%d. %s (%s, %s)\n", i+1, s.Name, roleText(s, titles), kindText[s.Kind])
		if s.Instructions != "" {
			b.WriteString("   Instructions: " + indent(s.Instructions) + "\n")
		}
		if out := outgoing(w, s.ID, byID); out != "" {
			b.WriteString("   " + out + "\n")
		}
	}
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
			fmt.Fprintf(&b, "  %s -.->|\"%s\"| %s\n", from, mermaidText(firstNonEmpty(e.Label, "reject")), to)
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
		return "user"
	case s.Role == leadRole:
		return "lead"
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
		return "the user"
	case s.Role == leadRole:
		return "the lead: you"
	}
	if t, ok := titles[s.Role]; ok && t != "" && t != s.Role {
		return "role " + t + " (" + s.Role + ")"
	}
	if _, ok := titles[s.Role]; ok {
		return "role " + s.Role
	}
	return "role " + s.Role + ", no longer on the team: use a suitable role instead"
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
			branch = append(branch, "if "+e.Label+" → "+name)
		case e.Label != "":
			name += " (" + e.Label + ")"
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
		parts = append(parts, "then → "+strings.Join(next, ", "))
	}
	if len(branch) > 0 {
		parts = append(parts, "take only one by condition: "+strings.Join(branch, "; "))
	}
	if len(back) > 0 {
		parts = append(parts, "on failure, back to → "+strings.Join(back, ", "))
	}
	return strings.Join(parts, "; ")
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
