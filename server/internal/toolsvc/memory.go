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

// Project auto memory tools (architecture.md §6.7): memory_list / memory_read for everyone,
// memory_write / memory_delete for the coordinator and leads, remember as a MEMORY.md shortcut.

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

func emitMemory(ctx context.Context, tx *core.Tx, projectID, actor, op, name string) error {
	_, err := tx.Emit(ctx, projectID, nil, core.EvMemory, actor, map[string]any{"op": op, "name": name})
	return err
}

func (s *Service) memoryList(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	files, err := store.ListMemoryFiles(ctx, tx, c.emp().ProjectID)
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
	f, err := store.GetMemoryFile(ctx, tx, c.emp().ProjectID, in.Name)
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
	desc := ""
	if in.Description != nil {
		desc = *in.Description
	}
	f, created, err := store.WriteMemoryFile(ctx, tx, c.emp().ProjectID, store.MemoryWrite{Name: in.Name, Description: desc,
		Content: in.Content, By: author(c)})
	if err == nil {
		err = emitMemory(ctx, tx, c.emp().ProjectID, c.emp().Name, "write", f.Name)
	}
	return memoryResult(err, func() Result {
		verb := "已更新"
		if created {
			verb = "已创建"
		}
		return okJSON(verb+" "+f.Name, protocol.MemoryWriteResult{Name: f.Name, Created: created})
	})
}

func (s *Service) memoryDelete(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	var in protocol.MemoryDeleteInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	err := store.DeleteMemoryFile(ctx, tx, c.emp().ProjectID, in.Name, author(c))
	if err == nil {
		err = emitMemory(ctx, tx, c.emp().ProjectID, c.emp().Name, "delete", in.Name)
	}
	return memoryResult(err, func() Result { return okText("已删除 " + in.Name) })
}

// remember appends a one-line note to MEMORY.md.
func (s *Service) remember(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	var in protocol.RememberInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	err := store.AppendMemoryNote(ctx, tx, c.emp().ProjectID, in.Text, author(c))
	if err == nil {
		err = emitMemory(ctx, tx, c.emp().ProjectID, c.emp().Name, "write", store.MemoryIndex)
	}
	return memoryResult(err, func() Result { return okText("已记入 MEMORY.md") })
}

// rememberSummary files a finished thread's summary under thread-summaries.md.
func (s *Service) rememberSummary(ctx context.Context, tx *core.Tx, t core.Thread, summary string) error {
	if err := store.AppendThreadSummary(ctx, tx, t.ProjectID, t.No, t.Title, summary); err != nil {
		return err
	}
	return emitMemory(ctx, tx, t.ProjectID, fmt.Sprintf("#%d", t.No), "write", store.ThreadSummaries)
}
