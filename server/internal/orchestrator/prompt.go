package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/ghops"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// memoryLimit caps the project memory injected into prompts (architecture.md Q4: 8 KB).
const memoryLimit = 8 << 10

const coordinatorGuide = `你是这个项目的项目统筹（coordinator）。你不能读写文件、不能执行命令，只能使用平台工具：
- open_thread(title, brief, repos?)：把一件具体的工作交给一个新的讨论串（组长 + 成员）去做，brief 写清目标、约束和验收标准；项目绑定了多个仓库时用 repos=[{repo, base?}] 指出要动的仓库，项目背景里写了基准分支的仓库要把 base 一并带上；
- list_threads / thread_status(no)：查看讨论串的进展；message_thread(no, text)：给某个讨论串的组长发话；
- ask_user：向用户提问（不阻塞，答案会作为消息回到这里）。要用户在几个方案里选时用 questions=[{question, header, options:[{label, description, recommended?}], multi_select?}]（1–4 个问题，每个 2–4 个选项，最多推荐一个；用户总能写“其他”），简单问题用 question(+options)；
- TodoWrite：事情多时列一个待办清单，用户在对话里能看到进度；
- remember(text)：把需要长期记住的项目事实写进项目记忆；set_status(text)：用一句中文说明你在做什么。
讨论串状态变化会以 “[Thread #12 · 标题 · 状态] …” 的消息通知你；只在用户需要知道时才对用户说话，回复简洁。`

const leadGuide = `你是讨论串 #%d「%s」的组长（lead）。你只能读代码（Read/Glob/Grep、只读 git 命令），不能改文件。
工作方式：
1. 先用 update_plan(steps) 写出主任务计划；计划有变就再调用一次（标题不变的步骤会保留）。
2. 用 assign_task(assignee, instruction, main, depends_on?, kind?) 拆小任务：assignee 写岗位名（如 dev）进任务池由空闲成员领取，写实例名（如 dev-2）则点名指派；main 写所属主任务的标题。
3. 成员完成后你会收到 “[Task t7 · dev-1 · done] 总结”；审查后用 enqueue_merge(task_id) 把成员分支放进合并队列。
4. 成员的提问 “[Question q3 …]” 尽快用 reply_to 回答；需要用户决定时用 ask_user（不阻塞；有几个方案让用户选时用 questions 结构化提问，带上每个选项的说明和推荐）。
   你的 TodoWrite 清单会显示在用户的对话里，适合跟踪讨论串的进度。
   成员的权限请求 “[Permission request q5 …]” 尽快用 review_permission(request_id, decision, reason) 判断：任务需要且无害的 allow，越界或危险的 deny（理由会发给成员），拿不准、涉及秘密、生产环境、费用或不可逆操作的选 ask_user 交给用户。
5. 全部主任务完成后 open_pr(title, body)，平台给每个有改动的仓库各开一个 PR；最后 finish(summary) 写总结。可以用 publish_deliverable 登记文档、设计等交付物。
项目可能绑定多个仓库：你的工作目录下每个仓库一个子目录（见“## 仓库”）。讨论串分支只在真正有改动的仓库里创建，默认从仓库的默认分支切出。
项目背景或用户要求里写了某个仓库的基准分支时，第一次给这个仓库派任务就在 assign_task 里写上 repo 和 base（之后 enqueue_merge / open_pr 也可以带 repo、base）；没写的仓库不用传 base。`

const memberGuide = `你是讨论串 #%d 的成员 %s（岗位：%s）。你在自己的工作目录里工作，每个仓库一个子目录，分支都是 %s；改了哪个仓库就在哪个子目录里 git commit。
- 收到任务后专心完成；做完必须调用 complete_task(task_id, summary)（平台会先替你推送每个有新提交的仓库）；做不完用 status=failed 说明原因。
- 需要中途推送用 push_branch()；不要自己 git push（你没有写权限）。
- 有疑问用 ask_lead（阻塞到组长回答）；需要用户决定用 ask_user（让用户选方案时用 questions，选项带说明和推荐）；给队友留言用 send_message，用 read_messages 看留言；list_team / get_task 查看团队和任务。
- 切换工作内容时调用 set_status(text)，用一句中文说明你在干什么。`

// docsCache keeps each repository's CLAUDE.md (read from its default branch) for a while.
type docsCache struct {
	mu    sync.Mutex
	items map[string]docEntry
}

type docEntry struct {
	text string
	at   time.Time
}

var claudeDocs = &docsCache{items: map[string]docEntry{}}

// projectDocs returns the CLAUDE.md of every repository, each under its name ("" when none has one).
func (o *Orchestrator) projectDocs(ctx context.Context, p store.Project) string {
	repos := o.Repos(ctx, p)
	var parts []string
	for _, r := range repos {
		text := o.repoDoc(ctx, p, r)
		if text == "" {
			continue
		}
		if len(repos) > 1 {
			text = "### " + r.Slug + "\n" + text
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, "\n\n")
}

// repoDoc reads one repository's CLAUDE.md on its default branch (cached 10 minutes, "" on failure).
func (o *Orchestrator) repoDoc(ctx context.Context, p store.Project, r ghops.Repo) string {
	key := p.ID + "@" + r.Slug
	claudeDocs.mu.Lock()
	e, ok := claudeDocs.items[key]
	claudeDocs.mu.Unlock()
	if ok && time.Since(e.at) < 10*time.Minute {
		return e.text
	}
	fctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	text, err := o.gh.FileContent(fctx, r, "CLAUDE.md", o.DefaultBranch(fctx, r))
	if err != nil {
		o.log.Debug("read CLAUDE.md", "project_id", p.ID, "repo", r.Slug, "err", err)
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
	if doc := o.projectDocs(ctx, p); doc != "" {
		parts = append(parts, "## 仓库 CLAUDE.md（默认分支）\n"+doc)
	}
	if repos := o.reposSection(ctx, q, e, t, p); repos != "" {
		parts = append(parts, repos)
	}
	if p.Context != "" {
		parts = append(parts, "## 项目背景（基准分支、约定等以这里为准）\n"+p.Context)
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
