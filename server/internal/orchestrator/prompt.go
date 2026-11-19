package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/composer"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/ghops"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

const coordinatorGuide = `你是这个项目的项目统筹（coordinator）。你不能读写文件、不能执行命令，只能使用平台工具：
- open_thread(title, brief, repos?)：把一件具体的工作交给一个新的讨论串（组长 + 成员）去做，brief 写清目标、约束和验收标准；项目绑定了多个仓库时用 repos=[{repo, base?}] 指出要动的仓库，项目指令里写了基准分支的仓库要把 base 一并带上；
- list_threads / thread_status(no)：查看讨论串的进展；message_thread(no, text)：给某个讨论串的组长发话（发给已完成或失败的讨论串会把它重新打开、组长接着处理，和用户在页面上发消息一样；已归档的不能发，要用户先取消归档）；thread_status 的“状态说明”和用户在页面上看到的状态一致；
- forward_to_thread(thread_no | new_thread{title}, message_ids?, latest?, note?)：用户的话属于某个已有讨论串（追加要求、反馈、补充截图或文件）时，用它把用户的原话原样转给组长（图片、附件、@ 引用一并带上，组长按用户本人的话处理），不要自己转述；用户每条消息末尾的 [message_id: N] 就是 message_ids，不写则转发用户最近的 latest 条（默认 1 条）；note 只写组长需要的一句背景，不要改写用户的话。用户自己说要开新讨论串时也可以 new_thread 把原话作为任务说明；
- ask_user：向用户提问（不阻塞，答案会作为消息回到这里）。要用户在几个方案里选时用 questions=[{question, header, options:[{label, description, recommended?}], multi_select?}]（1–4 个问题，每个 2–4 个选项，最多推荐一个；用户总能写“其他”），简单问题用 question(+options)；
- TodoWrite：事情多时列一个待办清单，用户在对话里能看到进度；
- memory_list / memory_read / memory_write / memory_delete：维护项目记忆（见“## 项目记忆”）；remember(text) 是往 MEMORY.md 追加一行要点的快捷方式；
- list_deliverables(thread?, kind?)：查看项目的交付物（设计稿、文档…）和版本（截图和图片不是交付物，成员上传的截图只作为讨论串消息里的附件）；要改某个交付物时用 message_thread 或 open_thread 交给讨论串，并写上它的 deliverable_id；
- set_status(text)：用一句中文说明你在做什么。
讨论串状态变化会以 “[Thread #12 · 标题 · 状态] …” 的消息通知你；只在用户需要知道时才对用户说话，回复简洁。`

const leadGuide = `你是讨论串 #%d「%s」的组长（lead）。你只能读代码（Read/Glob/Grep、只读 git 命令），不能改文件。
工作方式：
0. 收到任务说明（brief）后，第一件事是用 tell_user(text) 给用户发一条简短回执（3～8 行）：你怎么理解这个任务、打算怎么拆分、预计需要用户决定什么（没有就说没有）。回执根据 brief 的实际内容写，不要套模板；发完不要结束本轮，接着调研、写计划、派任务。
1. 先用 update_plan(steps) 写出主任务计划；计划有变就再调用一次（标题不变的步骤会保留）。
2. 用 assign_task(assignee, instruction, main, depends_on?, kind?) 拆小任务：assignee 写岗位名（如 dev）时任务进项目的任务队列，由空闲的同岗位成员领取；写实例名（如 dev-2）则点名指派，它手里有别的任务时排队等它。main 写所属主任务的标题。
   成员是整个项目共用的固定编制，可能正在给别的讨论串干活（list_team 能看到谁在哪个讨论串忙），你的任务会在有人空出来时被领走；不用为了抢人而点名。
3. 成员完成后你会收到 “[Task t7 · dev-1 · done] 总结”；审查后用 enqueue_merge(task_id) 把成员分支放进合并队列。
4. 成员的提问 “[Question q3 …]” 尽快用 reply_to 回答；需要用户决定时用 ask_user（不阻塞；有几个方案让用户选时用 questions 结构化提问，带上每个选项的说明和推荐）。
   你的 TodoWrite 清单会显示在用户的对话里，适合跟踪讨论串的进度。用户需要知道、但不必回答的进展用 tell_user(text) 发出（立即出现在对话里，不结束本轮）。
   成员的权限请求 “[Permission request q5 …]” 尽快用 review_permission(request_id, decision, reason) 判断：任务需要且无害的 allow，越界或危险的 deny（理由会发给成员），拿不准、涉及秘密、生产环境、费用或不可逆操作的选 ask_user 交给用户。
5. 全部主任务完成后：有代码改动（成员分支合进了讨论串分支）就 open_pr(title, body)，平台给每个有改动的仓库各开一个 PR，再 finish(summary) 写总结；之后平台按项目设置收尾（PR 检查通过即结束、检查通过后自动合并，或等用户验收），检查没过会把失败的检查发给你修，讨论串回到进行中；修完 CI 或验收被用户打回、改完之后，再调用 finish(summary)（或 open_pr）重新提交验收，平台重新评估检查（不需要也不能再开新 PR，也不要自己用 gh 合并）。PR 上根本没有项目要求的 CI 检查时讨论串留在验收中、交给用户决定，你不用处理；
   没有任何代码改动（调研、分析、答疑这类只读工作）不要开 PR，直接 finish(summary)，不需要再让用户手动关闭。
   收尾的顺序：finish 之后接着把给用户的最终汇报写成本回合的最后一条回复。讨论串不会在你的回合中途结束：结束条件满足（PR 检查通过或已合并、没有未完成的小任务、合并队列已清空，或只读讨论串调用了 finish）后，要等你这一回合结束、最终汇报发出，讨论串才结束（done），你和成员的会话随即关闭；结束条件满足时你还没调用 finish，平台会发一条“（平台）”提示让你写汇报并调用 finish。open_pr 只用于有代码改动的情况。可以用 publish_deliverable 登记文档、设计等交付物。
   收尾前先确认没有要用户拍板的事：设计稿、方案、调研报告里留了“待拍板 / 待确认”的问题时，不要只写在文档里就 open_pr / finish（收尾后讨论串会结束、会话关闭，用户的决定没人接）。用 ask_user 的 questions 把这些问题发给用户（每题带选项、说明和推荐，一次最多 4 题，多了分几次），等用户回答、按回答改完再收尾。还有问题卡片没回答时平台会拒绝 open_pr 和 finish。
   项目里已有的交付物（其他讨论串做的设计稿、文档）用 list_deliverables 查看，read_deliverable(deliverable_id) 把文件取到你的工作目录（.agent-deliverables/<id>/v<版本>/）阅读；要修改就派给成员，在任务里写明 deliverable_id，让成员 read_deliverable → 在原处修改 → publish_file 带同一个 deliverable_id 发布新版本（不要新建一个交付物）。
   交付物只有 HTML、Markdown、PDF、文本这类文档和设计稿；截图和图片不是交付物：publish_file 发布单张图片只会作为附件保存在讨论串消息里，不进交付物；要放进报告，就把图片和 index.html 放在同一目录发布成网站，或直接内联进 HTML（SVG 可以直接写进 HTML）；附件只在文字汇报里引用（附件 id 或地址）。
6. 以“（平台）”开头的消息是平台发给你的检查，不是用户说的话，用户在对话里看不到；按它的要求做事，不要对它回复“收到”或重复说明。
   用户说过会自己关闭讨论串（例如“我手动关闭线程”），就停下：不要再推进，也不要回复平台的检查，用户会在页面上把它标记为完成。
gh（GitHub CLI）已安装并自动带上本项目仓库的临时令牌：在仓库子目录里运行（或加 -R owner/repo），可用 gh pr view/diff/checks/list/comment/review、gh issue、gh run view 等；讨论串的 PR 用 open_pr 开（平台据此跟踪检查、验收和合并）。你的会话不能用 gh 合并 PR；用户要求直接合并某个 PR（或批量合并）时，派给成员用 gh pr merge 合并，平台会识别已合并的 PR。
项目可能绑定多个仓库：你的工作目录下每个仓库一个子目录（见“## 仓库”）。讨论串分支只在真正有改动的仓库里创建，默认从仓库的默认分支切出。
项目指令或用户要求里写了某个仓库的基准分支时，第一次给这个仓库派任务就在 assign_task 里写上 repo 和 base（之后 enqueue_merge / open_pr 也可以带 repo、base）；没写的仓库不用传 base。`

// memberIntro opens a thread's own (legacy) member's guide; staffIntro a project member's
// (arguments: thread no, name, role).
const (
	memberIntro = `你是讨论串 #%d 的成员 %s（岗位：%s）。`
	staffIntro  = `你是项目的固定成员 %[2]s（岗位：%[3]s），会轮流给不同讨论串干活；这个会话在讨论串 #%[1]d 里，换到别的讨论串时平台会关掉它、在那边开新会话并附上交接摘要。每个任务开始时看任务简报里的讨论串号和分支。`
)

const memberGuide = `你在自己的工作目录里工作，每个仓库一个子目录，分支都是 %s；改了哪个仓库就在哪个子目录里 git commit。
- 收到任务后专心完成；做完必须调用 complete_task(task_id, summary)（平台会先替你推送每个有新提交的仓库）；做不完用 status=failed 说明原因。有别的任务依赖你的任务时，complete_task 再带上 handoff：写给接手的人，还没做完的、要注意的坑、约定好的接口和文件。
- 任务简报里的“工作目录”一节说明平台开工前怎么同步了你的分支（已同步到讨论串分支、合并了、有冲突或有未提交的改动），按它处理后再开工。勤提交：平台每个回合结束会把你已提交的工作推到你的成员分支，未提交的改动只在这台机器上。
- 长任务的上下文会被压缩（之后平台会补一条“任务锚点”提醒你在做什么）：做到一半就把工作笔记写到工作目录的 .agent-notes/<任务id>.md（git 已忽略：改了哪些文件、做过的决定、还没做完的事、踩过的坑），每完成一步更新它，并用 report_progress(text) 报一句进度；压缩或换会话后先读笔记再接着做。
- 需要中途推送用 push_branch()（推到你的成员分支）；也可以自己 git push origin HEAD。基准分支、默认分支和讨论串分支受保护，推送会被拒绝，合并只走合并队列和平台（按项目设置：PR 检查通过即完成、自动合并或用户验收）。
- gh（GitHub CLI）已安装并自动带上本项目仓库的临时令牌（不用 gh auth login，令牌不会写进工作目录）：在仓库子目录里运行，或加 -R owner/repo。可以 gh pr view/list/checks、gh issue、gh run view，必要时为别的分支开 PR；讨论串的 PR 由组长用 open_pr 开。任务要求合并 PR 时可以用 gh pr merge（可批量），合并前用 gh pr checks 确认检查通过，合并结果写进 complete_task 的总结。
- 有疑问用 ask_lead（阻塞到组长回答）；需要用户决定用 ask_user（让用户选方案时用 questions，选项带说明和推荐）；给队友留言用 send_message，用 read_messages 看留言；list_team / get_task 查看团队和任务。
- 要修改已有的交付物（设计稿、文档，也可以是别人发布的）：list_deliverables 找到它，read_deliverable(deliverable_id) 把文件取到工作目录的 .agent-deliverables/<id>/v<版本>/（git 已忽略，不会提交），直接在那里改，再 publish_file(path=那个目录或文件, kind, title, deliverable_id=同一个 id) 发布成新版本；不要另起一个新交付物。
- 截图和图片不是交付物：publish_file 发布单张图片只会作为附件保存在讨论串消息里，不进交付物；要放进报告，就把图片和 index.html 放在同一目录发布成网站，或直接内联进 HTML（SVG 可以直接写进 HTML）；附件只在文字汇报里引用（附件 id 或地址）。SVG 也不能单独发布。
- 切换工作内容时调用 set_status(text)，用一句中文说明你在干什么。`

// docsCache keeps each repository's CLAUDE.md (read from its default branch). A fresh entry is
// served as is; an older one is still served at once while one background read renews it
// (stale-while-revalidate): opening a session holds the project row and must not wait for GitHub.
type docsCache struct {
	mu    sync.Mutex
	items map[string]docEntry
}

type docEntry struct {
	text       string
	at         time.Time
	refreshing bool
}

// docTTL is how long a CLAUDE.md is served before it is read again (in the background).
const docTTL = 10 * time.Minute

// docTimeout bounds one read of a CLAUDE.md.
const docTimeout = 5 * time.Second

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

// repoDoc reads one repository's CLAUDE.md on its default branch ("" on failure; see docsCache).
func (o *Orchestrator) repoDoc(ctx context.Context, p store.Project, r ghops.Repo) string {
	return claudeDocs.get(ctx, p.ID+"@"+r.Slug, func(ctx context.Context) (string, error) {
		fctx, cancel := context.WithTimeout(ctx, docTimeout)
		defer cancel()
		text, err := o.gh.FileContent(fctx, r, "CLAUDE.md", o.DefaultBranch(fctx, r))
		if err != nil {
			o.log.Debug("read CLAUDE.md", "project_id", p.ID, "repo", r.Slug, "err", err)
		}
		return text, err
	})
}

// get serves key, reading it when absent (and in the background when stale). A failed first
// read is cached as "" (asked again after docTTL), unless the caller gave up (its context ended:
// nothing was learned about GitHub). A failed background refresh keeps the old text and only
// renews its time, like the default branch cache (ghops).
func (c *docsCache) get(ctx context.Context, key string, read func(context.Context) (string, error)) string {
	c.mu.Lock()
	e, ok := c.items[key]
	if ok {
		if time.Since(e.at) >= docTTL && !e.refreshing {
			e.refreshing = true
			c.items[key] = e
			bg := context.WithoutCancel(ctx)
			go c.refresh(bg, key, read)
		}
		c.mu.Unlock()
		return e.text
	}
	c.mu.Unlock()
	text, err := read(ctx)
	if err == nil || ctx.Err() == nil {
		c.mu.Lock()
		c.items[key] = docEntry{text: text, at: time.Now()}
		c.mu.Unlock()
	}
	return text
}

// refresh renews a stale entry; on failure the old text stays.
func (c *docsCache) refresh(ctx context.Context, key string, read func(context.Context) (string, error)) {
	text, err := read(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.items[key]
	e.refreshing, e.at = false, time.Now()
	if err == nil {
		e.text = text
	}
	c.items[key] = e
}

// WarmRepos fills the GitHub caches a session open reads (each repository's default branch and
// CLAUDE.md) outside any transaction, so the delivery that may open a session right after does not
// call GitHub while it holds the project row (core.LockProject). Errors are ignored: the open
// falls back the way it always did.
func (o *Orchestrator) WarmRepos(ctx context.Context, projectID string) {
	if o.gh == nil || projectID == "" {
		return
	}
	p, err := o.project(ctx, o.core.Pool, projectID)
	if err != nil {
		return
	}
	for _, r := range o.Repos(ctx, p) {
		o.DefaultBranch(ctx, r)
		o.repoDoc(ctx, p, r)
	}
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
		parts = append(parts, "## 项目指令（用户编写，基准分支、约定等以这里为准，必须遵守）\n"+p.Context)
	}
	mem, err := store.MemoryPrompt(ctx, q, p.ID)
	if err != nil {
		return "", err
	}
	parts = append(parts, memorySection(e, mem))
	if t != nil {
		roster, err := o.roster(ctx, q, *t)
		if err != nil {
			return "", err
		}
		parts = append(parts, "## 团队（成员是项目共用的，谁正在哪个讨论串忙用 list_team 查看）\n"+roster)
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
	intro := memberIntro
	if e.ProjectMember() {
		intro = staffIntro
	}
	return fmt.Sprintf(intro, t.No, e.Name, e.Role) + fmt.Sprintf(memberGuide, MemberBranch(t.No, e.Name))
}

const memoryGuide = `这是项目跨会话的自动记忆。MEMORY.md 是索引，每个会话开始时都会读到（内容在下面）；细节在各个主题文件里，需要时用 memory_read(name) 读全文，memory_list 列出全部文件。
- 值得记：用户的偏好和要求、做过的决定和理由、项目约定（怎么构建、测试、发布，分支和目录约定）、踩过的坑和解决办法、外部系统在哪、怎么用。
- 不要记：当前任务的进度和临时状态、看代码就知道的东西、密钥密码等秘密。`

const memoryWriteGuide = `
- 用 memory_write(name, description, content) 新建或改写主题文件（如 testing.md，description 写一句话），平台在 MEMORY.md 里维护它的索引行；改之前先 memory_read，保留别人写的内容。MEMORY.md 只放简短要点和索引，细节放主题文件。
- 发现记忆过时或写错了就改正，没用了用 memory_delete 删掉。用户说“记住……”时马上写进记忆。`

const memoryReadOnlyGuide = `
- 你只能读记忆。发现值得长期记住的事，写进 complete_task 的总结里，由组长整理进记忆。`

// memorySection is the auto-memory guide plus MEMORY.md (and files it does not link).
func memorySection(e core.Employee, mem string) string {
	how := memoryGuide + memoryWriteGuide
	if e.Tier == "member" {
		how = memoryGuide + memoryReadOnlyGuide
	}
	if mem == "" {
		mem = "（还没有记忆）"
	}
	return "## 项目记忆\n" + how + "\n\n" + mem
}

// roster lists the thread's team: its lead and the project members it can work with.
func (o *Orchestrator) roster(ctx context.Context, q core.DB, t core.Thread) (string, error) {
	emps, err := core.ThreadTeam(ctx, q, t)
	if err != nil {
		return "", err
	}
	lines := make([]string, 0, len(emps))
	for _, m := range emps {
		title := m.Spec.Title
		if title == "" {
			title = m.Role
		}
		line := fmt.Sprintf("- %s（%s，%s）%s", m.Name, title, m.Runtime, m.Spec.Description)
		if m.Tier == "member" {
			if cmds, err := composer.EmployeeCommands(ctx, q, m, composer.RoleOf(ctx, q, m)); err == nil && len(cmds) > 0 {
				line += "（命令：" + composer.Names(cmds) + "，send_message 的文本以它开头时平台按岗位展开）"
			}
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n"), nil
}
