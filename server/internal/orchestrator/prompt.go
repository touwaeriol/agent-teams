package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// memoryLimit caps the project memory injected into prompts (architecture.md Q4: 8 KB).
const memoryLimit = 8 << 10

const coordinatorGuide = `你是这个项目的项目统筹（coordinator）。你不能读写文件、不能执行命令，只能使用平台工具：
- open_thread(title, brief)：把一件具体的工作交给一个新的讨论串（组长 + 成员）去做，brief 写清目标、约束和验收标准；
- list_threads / thread_status(no)：查看讨论串的进展；message_thread(no, text)：给某个讨论串的组长发话；
- ask_user(question, options?)：向用户提问（不阻塞，答案会作为消息回到这里）；
- remember(text)：把需要长期记住的项目事实写进项目记忆；set_status(text)：用一句中文说明你在做什么。
讨论串状态变化会以 “[Thread #12 · 标题 · 状态] …” 的消息通知你；只在用户需要知道时才对用户说话，回复简洁。`

const leadGuide = `你是讨论串 #%d「%s」的组长（lead）。你只能读代码（Read/Glob/Grep、只读 git 命令），不能改文件。
工作方式：
1. 先用 update_plan(steps) 写出主任务计划；计划有变就再调用一次（标题不变的步骤会保留）。
2. 用 assign_task(assignee, instruction, main, depends_on?, kind?) 拆小任务：assignee 写岗位名（如 dev）进任务池由空闲成员领取，写实例名（如 dev-2）则点名指派；main 写所属主任务的标题。
3. 成员完成后你会收到 “[Task t7 · dev-1 · done] 总结”；审查后用 enqueue_merge(task_id) 把成员分支放进合并队列。
4. 成员的提问 “[Question q3 …]” 尽快用 reply_to 回答；需要用户决定时用 ask_user（不阻塞）。
5. 全部主任务完成后 open_pr(title, body)，最后 finish(summary) 写总结。可以用 publish_deliverable 登记文档、设计等交付物。`

const memberGuide = `你是讨论串 #%d 的成员 %s（岗位：%s）。你在自己的 worktree 里工作（分支 %s），改完请 git commit。
- 收到任务后专心完成；做完必须调用 complete_task(task_id, summary)（平台会先替你推送分支）；做不完用 status=failed 说明原因。
- 需要中途推送用 push_branch()；不要自己 git push（你没有写权限）。
- 有疑问用 ask_lead（阻塞到组长回答）；需要用户决定用 ask_user；给队友留言用 send_message，用 read_messages 看留言；list_team / get_task 查看团队和任务。
- 切换工作内容时调用 set_status(text)，用一句中文说明你在干什么。`

// docsCache keeps each project's CLAUDE.md (read from the base branch) for a while.
type docsCache struct {
	mu    sync.Mutex
	items map[string]docEntry
}

type docEntry struct {
	text string
	at   time.Time
}

var claudeDocs = &docsCache{items: map[string]docEntry{}}

// projectDoc returns the repository's CLAUDE.md on the base branch (cached 10 minutes, "" on failure).
func (o *Orchestrator) projectDoc(ctx context.Context, p store.Project) string {
	if o.gh == nil {
		return ""
	}
	repo, err := o.gh.RepoOf(ctx, p)
	if err != nil {
		return ""
	}
	key := p.ID + "@" + repo.Base
	claudeDocs.mu.Lock()
	e, ok := claudeDocs.items[key]
	claudeDocs.mu.Unlock()
	if ok && time.Since(e.at) < 10*time.Minute {
		return e.text
	}
	fctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	text, err := o.gh.FileContent(fctx, repo, "CLAUDE.md", repo.Base)
	if err != nil {
		o.log.Debug("read CLAUDE.md", "project_id", p.ID, "err", err)
	}
	claudeDocs.mu.Lock()
	claudeDocs.items[key] = docEntry{text: text, at: time.Now()}
	claudeDocs.mu.Unlock()
	return text
}

// systemAppend builds the prompt appended to the claude_code preset (§6.1).
func (o *Orchestrator) systemAppend(ctx context.Context, q core.DB, e core.Employee, t *core.Thread, p store.Project) (string, error) {
	parts := []string{guide(e, t)}
	if e.Spec.Instructions != "" {
		parts = append(parts, "## 岗位说明\n"+e.Spec.Instructions)
	}
	if doc := o.projectDoc(ctx, p); doc != "" {
		parts = append(parts, "## 仓库 CLAUDE.md（基准分支）\n"+doc)
	}
	if p.Context != "" {
		parts = append(parts, "## 项目背景\n"+p.Context)
	}
	mem, err := o.memory(ctx, p.ID)
	if err != nil {
		return "", err
	}
	if mem != "" {
		parts = append(parts, "## 项目记忆\n"+mem)
	}
	if t != nil {
		roster, err := o.roster(ctx, q, t.ID)
		if err != nil {
			return "", err
		}
		parts = append(parts, "## 本讨论串成员\n"+roster)
	}
	return strings.Join(parts, "\n\n"), nil
}

func guide(e core.Employee, t *core.Thread) string {
	switch {
	case e.Tier == "coordinator" || t == nil:
		return coordinatorGuide
	case e.Tier == "lead":
		return fmt.Sprintf(leadGuide, t.No, t.Title)
	}
	return fmt.Sprintf(memberGuide, t.No, e.Name, e.Role, MemberBranch(t.No, e.Name))
}

// memory renders pinned items and the latest ones, capped at memoryLimit.
func (o *Orchestrator) memory(ctx context.Context, projectID string) (string, error) {
	items, err := o.store.ListMemory(ctx, projectID)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for i, m := range items {
		if i >= 50 && !m.Pinned {
			break
		}
		line := fmt.Sprintf("- [%s] %s\n", m.Source, m.Text)
		if b.Len()+len(line) > memoryLimit {
			break
		}
		b.WriteString(line)
	}
	return strings.TrimSpace(b.String()), nil
}

func (o *Orchestrator) roster(ctx context.Context, q core.DB, threadID string) (string, error) {
	emps, err := core.ThreadEmployees(ctx, q, threadID)
	if err != nil {
		return "", err
	}
	lines := make([]string, 0, len(emps))
	for _, m := range emps {
		title := m.Spec.Title
		if title == "" {
			title = m.Role
		}
		lines = append(lines, fmt.Sprintf("- %s（%s，%s）%s", m.Name, title, m.Runtime, m.Spec.Description))
	}
	return strings.Join(lines, "\n"), nil
}
