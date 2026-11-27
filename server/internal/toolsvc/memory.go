package toolsvc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Auto memory tools (architecture.md §6.7): memory_list / memory_read for everyone, memory_write /
// memory_edit / memory_delete for the coordinator and leads, remember as a MEMORY.md shortcut.
// scope user is the user's own memory (every project of the user reads it). With the shared
// folder on, a change lands in the rows and is written into the folder right after the call.

// author names the writer: "coordinator", or "lead-1 · #3" inside a thread.
func author(c *call) string {
	if c.thread != nil {
		return fmt.Sprintf("%s · #%d", c.emp().Name, c.thread.No)
	}
	return c.emp().Name
}

// memoryResult turns rejected writes and missing files into answers for the model.
func memoryResult(err error, ok func() Result) (Result, error) {
	switch {
	case err == nil:
		return ok(), nil
	case store.IsMemoryError(err):
		return failText("%s", err.Error()), nil
	case errors.Is(err, store.ErrNotFound) || errors.Is(err, core.ErrNotFound):
		return failText("没有这个记忆文件，先用 memory_list 看看有哪些"), nil
	}
	return Result{}, err
}

// memoryScope is the scope a call names: the project's by default, or its owner's own.
func memoryScope(c *call, scope *protocol.MemoryScope) store.MemoryScope {
	if scope != nil && *scope == protocol.MemoryScopeUser {
		return store.UserMemory(c.sc.OwnerID)
	}
	return store.ProjectMemory(c.emp().ProjectID)
}

// changed announces a change and has the folder written out after the transaction.
func (s *Service) changed(ctx context.Context, tx *core.Tx, sc store.MemoryScope, actor, op, name string) error {
	tx.AfterCommit(func(context.Context) { s.core.Memory.Changed(sc) })
	return tx.EmitMemory(ctx, sc, actor, op, name)
}

func (s *Service) memoryList(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	var in protocol.MemoryListInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	sc := memoryScope(c, in.Scope)
	files, err := store.ListMemoryFiles(ctx, tx, sc)
	if err != nil {
		return Result{}, err
	}
	out := protocol.MemoryListResult{Files: make([]protocol.MemoryFileBrief, 0, len(files))}
	lines := make([]string, 0, len(files))
	for _, f := range files {
		out.Files = append(out.Files, protocol.MemoryFileBrief{Name: f.Name, Description: f.Description, Size: f.Size,
			UpdatedBy: f.UpdatedBy, UpdatedAt: f.UpdatedAt.Format(time.RFC3339)})
		lines = append(lines, fmt.Sprintf("- %s（%d 字，%s 更新）%s", f.Name, f.Size, f.UpdatedBy, f.Description))
	}
	if len(lines) == 0 {
		return okJSON("还没有记忆文件", out), nil
	}
	return okJSON(strings.Join(lines, "\n"), out), nil
}

func (s *Service) memoryRead(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	var in protocol.MemoryReadInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	sc := memoryScope(c, in.Scope)
	f, err := store.GetMemoryFile(ctx, tx, sc, in.Name)
	return memoryResult(err, func() Result {
		text := f.Content
		if strings.TrimSpace(text) == "" {
			text = "（空文件）"
		}
		return okJSON(text, protocol.MemoryReadResult{Name: f.Name, Description: f.Description, Content: f.Content,
			UpdatedBy: f.UpdatedBy, UpdatedAt: f.UpdatedAt.Format(time.RFC3339)})
	})
}

func (s *Service) memoryWrite(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	var in protocol.MemoryWriteInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	sc := memoryScope(c, in.Scope)
	w := store.MemoryWrite{Name: in.Name, Description: deref(in.Description), Content: in.Content, By: author(c)}
	if in.Type != nil {
		w.Type = string(*in.Type)
	}
	f, created, err := store.WriteMemoryFile(ctx, tx, sc, w)
	if err == nil {
		err = s.changed(ctx, tx, sc, c.emp().Name, "write", f.Name)
	}
	return memoryResult(err, func() Result {
		verb := "已更新"
		if created {
			verb = "已创建"
		}
		return okJSON(verb+" "+f.Name, protocol.MemoryWriteResult{Name: f.Name, Created: created})
	})
}

// memoryEdit replaces part of a file, keeping its description and kind.
func (s *Service) memoryEdit(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	var in protocol.MemoryEditInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	sc := memoryScope(c, in.Scope)
	f, err := store.GetMemoryFile(ctx, tx, sc, in.Name)
	if err != nil {
		return memoryResult(err, nil)
	}
	content, n, err := replaceIn(f.Content, in.OldString, in.NewString, in.ReplaceAll != nil && *in.ReplaceAll)
	if err != nil {
		return memoryResult(err, nil)
	}
	_, _, err = store.WriteMemoryFile(ctx, tx, sc, store.MemoryWrite{Name: f.Name, Description: f.Description, Type: f.Type,
		Content: content, By: author(c)})
	if err == nil {
		err = s.changed(ctx, tx, sc, c.emp().Name, "write", f.Name)
	}
	return memoryResult(err, func() Result {
		return okJSON(fmt.Sprintf("已修改 %s（%d 处）", f.Name, n), protocol.MemoryEditResult{Name: f.Name, Replaced: n})
	})
}

// replaceIn replaces old with new: exactly one occurrence, or every one with all.
func replaceIn(content, old, new string, all bool) (string, int, error) {
	n := strings.Count(content, old)
	switch {
	case n == 0:
		return "", 0, store.NewMemoryError("文件里没有 old_string（要和文件内容逐字一致，先 memory_read 看原文）")
	case n > 1 && !all:
		return "", 0, store.NewMemoryError(fmt.Sprintf("old_string 在文件里出现了 %d 次；多带几行上下文让它唯一，或用 replace_all", n))
	}
	return strings.ReplaceAll(content, old, new), n, nil
}

func (s *Service) memoryDelete(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	var in protocol.MemoryDeleteInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	sc := memoryScope(c, in.Scope)
	err := s.core.Memory.Delete(ctx, tx, sc, in.Name, author(c))
	if err == nil {
		err = s.changed(ctx, tx, sc, c.emp().Name, "delete", in.Name)
	}
	return memoryResult(err, func() Result { return okText("已删除 " + in.Name) })
}

// remember appends a one-line note to MEMORY.md.
func (s *Service) remember(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	var in protocol.RememberInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	sc := memoryScope(c, in.Scope)
	err := store.AppendMemoryNote(ctx, tx, sc, in.Text, author(c))
	if err == nil {
		err = s.changed(ctx, tx, sc, c.emp().Name, "write", store.MemoryIndex)
	}
	where := "MEMORY.md"
	if sc.Kind == store.MemoryUser {
		where = "用户记忆 MEMORY.md"
	}
	return memoryResult(err, func() Result { return okText("已记入 " + where) })
}

// rememberSummary files a finished thread's summary under thread-summaries.md.
func (s *Service) rememberSummary(ctx context.Context, tx *core.Tx, t core.Thread, summary string) error {
	if err := store.AppendThreadSummary(ctx, tx, t.ProjectID, t.No, t.Title, summary); err != nil {
		return err
	}
	return s.changed(ctx, tx, store.ProjectMemory(t.ProjectID), fmt.Sprintf("#%d", t.No), "write", store.ThreadSummaries)
}
