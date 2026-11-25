package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/touwaeriol/agent-teams/server/internal/core"
)

// Forward limits: messages per forward, attachments they may carry in all, and how long a
// repeated forward of the same messages to the same place counts as a double click.
const (
	MaxForwardMessages    = 20
	MaxForwardAttachments = core.MaxToolAttachments
	forwardDedupeSeconds  = 30
)

// Forward errors (422 for the REST layer, explanations for the coordinator).
var (
	ErrForwardEmpty   = errors.New("请选择要转发的消息")
	ErrForwardNotUser = errors.New("只能转发你自己在这个项目对话里发的消息")
	ErrForwardTooMany = fmt.Errorf("一次最多转发 %d 条消息", MaxForwardMessages)
	ErrForwardFiles   = fmt.Errorf("所选消息一共带了太多附件（最多 %d 个），请少选几条", MaxForwardAttachments)
)

// ForwardRequest forwards the user's own project-chat messages to a thread's lead.
type ForwardRequest struct {
	UserID, ProjectID string
	// MessageIDs are the user's messages in the project's coordinator conversation; empty
	// with Latest > 0 takes the user's Latest most recent ones.
	MessageIDs []int64
	Latest     int
	// ThreadNo is the target thread; 0 opens a new thread titled NewTitle (or after the first
	// message) whose brief is the forwarded messages.
	ThreadNo int
	NewTitle string
	// Note is a short line from whoever forwards (the coordinator's 附言).
	Note string
	// By is "user" (from the project chat) or "coordinator" (forward_to_thread).
	By string
}

// ForwardResult is what a forward did.
type ForwardResult struct {
	Thread   core.Thread
	Messages []core.Message // the copies in the thread conversation
	Sources  []int64        // the forwarded project-chat message ids
	Created  bool           // a new thread was opened
	// Duplicate: the same messages went to the same place moments ago; nothing new was sent.
	Duplicate bool
}

// forwarded is one source message prepared for the thread: its text for the lead (attached
// text files and @ references expanded) and the attachments that travel as files.
type forwarded struct {
	src   core.Message
	text  string
	files []core.AttachmentRef
}

// Forward copies the user's messages into the thread conversation as the user's own messages
// (meta.forwarded_from) and delivers them to the lead as the user's words: a parked or closed
// lead is woken, a finished thread reopens, an archived one refuses (ErrArchived). Without a
// thread number it opens a thread whose brief is the messages. The project chat gets a note
// "已转发 N 条消息到 #n" (meta.forwarded_to), which also guards against double clicks.
func (o *Orchestrator) Forward(ctx context.Context, tx *core.Tx, req ForwardRequest) (ForwardResult, error) {
	conv, err := core.EnsureCoordinatorConversation(ctx, tx, req.UserID, req.ProjectID)
	if err != nil {
		return ForwardResult{}, err
	}
	src, err := forwardSources(ctx, tx, conv, req)
	if err != nil {
		return ForwardResult{}, err
	}
	key := forwardKey(req, src)
	if err := core.LockKey(ctx, tx, "forward:"+conv+":"+key); err != nil {
		return ForwardResult{}, err
	}
	if res, ok, err := o.recentForward(ctx, tx, conv, req.ProjectID, key); err != nil || ok {
		return res, err
	}
	items, err := o.prepareForward(ctx, tx, req, src)
	if err != nil {
		return ForwardResult{}, err
	}
	res, err := o.forwardTo(ctx, tx, req, conv, items)
	if err != nil {
		return res, err
	}
	for _, m := range src {
		res.Sources = append(res.Sources, m.ID)
	}
	return res, o.forwardNote(ctx, tx, req, conv, key, res)
}

// forwardSources loads the messages to forward: the user's own, of this coordinator conversation.
func forwardSources(ctx context.Context, tx *core.Tx, conv string, req ForwardRequest) ([]core.Message, error) {
	ids := uniqueIDs(req.MessageIDs)
	switch {
	case len(ids) == 0 && req.Latest <= 0:
		return nil, ErrForwardEmpty
	case len(ids) > MaxForwardMessages || req.Latest > MaxForwardMessages:
		return nil, ErrForwardTooMany
	case len(ids) == 0:
		list, err := core.LatestUserMessages(ctx, tx, conv, req.Latest)
		if err == nil && len(list) == 0 {
			err = ErrForwardEmpty
		}
		return list, err
	}
	list, err := core.UserMessagesByID(ctx, tx, conv, ids)
	if err == nil && len(list) != len(ids) {
		err = ErrForwardNotUser
	}
	return list, err
}

func uniqueIDs(ids []int64) []int64 {
	out := slices.Clone(ids)
	slices.Sort(out)
	return slices.Compact(out)
}

// forwardKey names the forward for the double-click guard: the target and the message ids.
func forwardKey(req ForwardRequest, src []core.Message) string {
	parts := []string{"new"}
	if req.ThreadNo > 0 {
		parts[0] = "#" + strconv.Itoa(req.ThreadNo)
	}
	for _, m := range src {
		parts = append(parts, strconv.FormatInt(m.ID, 10))
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, ",")))
	return hex.EncodeToString(sum[:12])
}

// recentForward answers a repeated forward with the first one's result.
func (o *Orchestrator) recentForward(ctx context.Context, tx *core.Tx, conv, projectID, key string) (ForwardResult, bool, error) {
	note, err := core.RecentNote(ctx, tx, conv, "forwarded_to", key, forwardDedupeSeconds)
	if errors.Is(err, core.ErrNotFound) {
		return ForwardResult{}, false, nil
	}
	if err != nil {
		return ForwardResult{}, false, err
	}
	to, _ := note.Meta["forwarded_to"].(map[string]any)
	no, _ := to["no"].(float64)
	t, err := core.ThreadByNo(ctx, tx, projectID, int(no))
	if err != nil {
		return ForwardResult{}, false, err
	}
	res := ForwardResult{Thread: t, Duplicate: true}
	ids, _ := to["message_ids"].([]any)
	for _, id := range ids {
		if f, ok := id.(float64); ok {
			res.Sources = append(res.Sources, int64(f))
		}
	}
	return res, true, nil
}

// prepareForward expands each message for the lead: attached text files inline, @ references
// resolved again (RefContext), images and PDFs kept as files.
func (o *Orchestrator) prepareForward(ctx context.Context, tx *core.Tx, req ForwardRequest, src []core.Message) ([]forwarded, error) {
	c := core.Conversation{ProjectID: &req.ProjectID}
	out := make([]forwarded, 0, len(src))
	total := 0
	for _, m := range src {
		refs := core.AttachmentsOf(m)
		total += len(refs)
		if total > MaxForwardAttachments {
			return nil, ErrForwardFiles
		}
		if err := o.useRefs(ctx, tx, req.ProjectID, refs); err != nil {
			return nil, err
		}
		text, files, err := o.inlineTexts(ctx, tx, c, m.Content, refs)
		if err != nil {
			return nil, err
		}
		if chips := core.RefChipsOf(m); len(chips) > 0 && o.RefContext != nil {
			text += o.RefContext(ctx, req.UserID, req.ProjectID, chips)
		}
		out = append(out, forwarded{src: m, text: text, files: files})
	}
	return out, nil
}

// forwardTo puts the copies in an existing thread (delivered to its lead) or opens a new one.
func (o *Orchestrator) forwardTo(ctx context.Context, tx *core.Tx, req ForwardRequest, conv string, items []forwarded) (ForwardResult, error) {
	if req.ThreadNo == 0 {
		return o.forwardNew(ctx, tx, req, conv, items)
	}
	t, err := core.ThreadByNo(ctx, tx, req.ProjectID, req.ThreadNo)
	if err != nil {
		return ForwardResult{}, err
	}
	if t, err = core.LockThread(ctx, tx, t.ID); err != nil {
		return ForwardResult{}, err
	}
	if err := o.reopenForMessage(ctx, tx, t); err != nil {
		return ForwardResult{}, err
	}
	lead, err := core.Lead(ctx, tx, t.ID)
	if err != nil {
		return ForwardResult{}, err
	}
	res := ForwardResult{Thread: t}
	recs := forwardRecords(req, conv, t.ID, items)
	for _, rec := range recs[:len(recs)-1] {
		m, err := insertEmitted(ctx, tx, t, rec)
		if err != nil {
			return res, err
		}
		res.Messages = append(res.Messages, m)
	}
	last := recs[len(recs)-1]
	m, err := o.Deliver(ctx, tx, lead, Delivery{Text: forwardText(req, items), Kind: "user", Priority: "next", Record: &last,
		Attachments: forwardFiles(items)})
	if err != nil {
		return res, err
	}
	res.Messages = append(res.Messages, m)
	return res, nil
}

// forwardNew opens a thread whose brief is the forwarded messages; the copies are recorded in
// its conversation and their images and PDFs go with the brief when the thread starts.
func (o *Orchestrator) forwardNew(ctx context.Context, tx *core.Tx, req ForwardRequest, conv string, items []forwarded) (ForwardResult, error) {
	title := strings.TrimSpace(req.NewTitle)
	if title == "" {
		title = titleFrom(items)
	}
	t, err := o.OpenThreadTx(ctx, tx, ThreadRequest{UserID: req.UserID, ProjectID: req.ProjectID, Title: title,
		Brief: forwardText(req, items), OpenedBy: req.By, ForwardedBrief: true})
	if err != nil {
		return ForwardResult{}, err
	}
	res := ForwardResult{Thread: t, Created: true}
	for _, rec := range forwardRecords(req, conv, t.ID, items) {
		m, err := insertEmitted(ctx, tx, t, rec)
		if err != nil {
			return res, err
		}
		res.Messages = append(res.Messages, m)
	}
	tx.AfterCommit(func(ctx context.Context) { go o.StartQueued(context.WithoutCancel(ctx), req.UserID) })
	return res, nil
}

func insertEmitted(ctx context.Context, tx *core.Tx, t core.Thread, rec core.NewMessage) (core.Message, error) {
	rec.Meta["thread_no"] = t.No
	m, err := core.InsertMessage(ctx, tx, rec)
	if err != nil {
		return m, err
	}
	return m, tx.EmitMessage(ctx, t.ProjectID, &t.ID, m)
}

// forwardRecords are the thread-conversation copies: the user's words, attachments and
// reference chips, plus meta.forwarded_from {conversation_id, message_id, message_ids, count,
// index, by, note?} (the note on the last one).
func forwardRecords(req ForwardRequest, conv, threadID string, items []forwarded) []core.NewMessage {
	ids := make([]int64, len(items))
	for i, it := range items {
		ids[i] = it.src.ID
	}
	out := make([]core.NewMessage, len(items))
	for i, it := range items {
		from := map[string]any{"conversation_id": conv, "message_id": it.src.ID, "message_ids": ids, "count": len(items),
			"index": i, "by": req.By}
		if note := strings.TrimSpace(req.Note); note != "" && i == len(items)-1 {
			from["note"] = note
		}
		meta := map[string]any{"forwarded_from": from}
		for _, k := range []string{"attachments", "refs"} {
			if v, ok := it.src.Meta[k]; ok {
				meta[k] = v
			}
		}
		out[i] = core.NewMessage{ConversationID: threadID, Role: "user", Author: "user", Kind: "chat", Content: it.src.Content, Meta: meta}
	}
	return out
}

// forwardText is what the lead gets: a header saying these are the user's own words, the
// forwarder's note, then each message.
func forwardText(req ForwardRequest, items []forwarded) string {
	var b strings.Builder
	by := "用户"
	if req.By == "coordinator" {
		by = "项目统筹"
	}
	fmt.Fprintf(&b, "[%s把用户在项目对话里说的 %d 条消息原样转发给你。下面是用户本人的原话，请当作用户直接对你说的话来处理]", by, len(items))
	if note := strings.TrimSpace(req.Note); note != "" {
		fmt.Fprintf(&b, "\n%s附言：%s", by, note)
	}
	for i, it := range items {
		if len(items) > 1 {
			fmt.Fprintf(&b, "\n\n—— 第 %d 条 ——", i+1)
		}
		b.WriteString("\n\n" + strings.TrimSpace(it.text))
		if n := len(it.files); n > 0 {
			fmt.Fprintf(&b, "\n（附 %d 个文件：%s）", n, fileNames(it.files))
		}
	}
	return b.String()
}

func fileNames(files []core.AttachmentRef) string {
	names := make([]string, len(files))
	for i, f := range files {
		names[i] = f.Name
	}
	return strings.Join(names, "、")
}

func forwardFiles(items []forwarded) []core.AttachmentRef {
	var out []core.AttachmentRef
	for _, it := range items {
		out = append(out, it.files...)
	}
	return out
}

// titleFrom names a new thread after the first forwarded words.
func titleFrom(items []forwarded) string {
	for _, it := range items {
		line := strings.TrimSpace(strings.SplitN(strings.TrimSpace(it.src.Content), "\n", 2)[0])
		if line != "" {
			return clipRunes(line, 40)
		}
	}
	return "转发的消息"
}

// forwardNote tells the project chat where the messages went (meta.forwarded_to).
func (o *Orchestrator) forwardNote(ctx context.Context, tx *core.Tx, req ForwardRequest, conv, key string, res ForwardResult) error {
	ids := res.Sources
	text := fmt.Sprintf("已转发 %d 条消息到 #%d · %s", len(res.Messages), res.Thread.No, res.Thread.Title)
	if res.Created {
		text = fmt.Sprintf("已转发 %d 条消息，新开讨论串 #%d · %s", len(res.Messages), res.Thread.No, res.Thread.Title)
	}
	author := "user"
	if req.By == "coordinator" {
		author = "coordinator"
	}
	m, err := core.InsertMessage(ctx, tx, core.NewMessage{ConversationID: conv, Role: "system", Author: author, Kind: "note", Content: text,
		Meta: map[string]any{"forwarded_to": map[string]any{"no": res.Thread.No, "key": key, "message_ids": ids, "count": len(ids),
			"created": res.Created, "by": req.By}}})
	if err != nil {
		return err
	}
	return tx.EmitMessage(ctx, req.ProjectID, nil, m)
}

// BriefAttachmentsKey marks the note open_subthread records in the new thread's conversation
// with the attachments its brief carries (meta.attachments); briefFiles collects them.
const BriefAttachmentsKey = "brief_attachments"

// briefFiles are the attachments that go to the lead with the brief: those of the user's
// message the thread was opened for, then those of the messages forwarded into it before it
// started, then those the parent's lead gave open_subthread (each attachment once; text files
// too: their content is inlined, and new daemons also get the file).
func briefFiles(ctx context.Context, tx *core.Tx, t core.Thread) ([]core.AttachmentRef, error) {
	list, err := core.MessagesWithMeta(ctx, tx, t.ID, "forwarded_from")
	if err != nil {
		return nil, err
	}
	given, err := core.MessagesWithMeta(ctx, tx, t.ID, BriefAttachmentsKey)
	if err != nil {
		return nil, err
	}
	list = append(list, given...)
	src, err := core.SourceMessage(ctx, tx, t)
	if err != nil {
		return nil, err
	}
	if src != nil {
		list = append([]core.Message{*src}, list...)
	}
	var out []core.AttachmentRef
	seen := map[string]bool{}
	for _, m := range list {
		for _, r := range core.AttachmentsOf(m) {
			if !seen[r.ID] {
				seen[r.ID] = true
				out = append(out, r)
			}
		}
	}
	return out, nil
}

// useRefs refuses expired attachments among refs and restarts the retention of the others (a
// forward is a use). Refs whose row is gone are left as they were (older messages).
func (o *Orchestrator) useRefs(ctx context.Context, tx *core.Tx, projectID string, refs []core.AttachmentRef) error {
	if len(refs) == 0 {
		return nil
	}
	ids := refIDs(refs)
	if name, err := core.ExpiredAttachment(ctx, tx, projectID, ids); err != nil {
		return err
	} else if name != "" {
		return fmt.Errorf("%w：%s", core.ErrAttachmentExpired, name)
	}
	return core.TouchAttachments(ctx, tx, projectID, ids, o.core.AttachmentTTL)
}

func refIDs(refs []core.AttachmentRef) []string {
	ids := make([]string, len(refs))
	for i, r := range refs {
		ids[i] = r.ID
	}
	return ids
}
