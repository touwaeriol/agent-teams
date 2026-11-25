package orchestrator

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
)

// FeatureAttachmentsV2 is the hello feature of daemons that take session.send/first_message
// attachments (any type, up to 200 MB, downloaded into .attachments/ with a presigned URL) and
// read_attachment's attachment_download (#58). Older daemons only get the legacy images.
const FeatureAttachmentsV2 = "attachments_v2"

// Legacy images (session.send images, daemons without attachments_v2): the protocol's ImageRef
// enum, at most 5 MB each and 8 per message. Anything else would make an old daemon's schema
// check drop the whole message, so legacyFit filters strictly.
const (
	maxLegacyImages  = 8
	legacyImageBytes = 5 << 20
)

var legacyImageTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true,
	core.PDFType: true}

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// attachmentRefs is the wire form of a delivery's attachments (session.send/first_message
// attachments; the daemon asks for the download URL when it delivers).
func attachmentRefs(list []core.AttachmentRef) []protocol.AttachmentRef {
	if len(list) == 0 {
		return nil
	}
	out := make([]protocol.AttachmentRef, len(list))
	for i, a := range list {
		out[i] = protocol.AttachmentRef{ID: a.ID, Name: a.Name, ContentType: a.ContentType, Size: a.Size}
		if sha256Hex.MatchString(a.SHA256) {
			sum := a.SHA256
			out[i].SHA256 = &sum
		}
	}
	return out
}

// mergeRefs is every attachment of a first message or send, each once: the legacy images of
// commands queued before #58 (or folded in from an old daemon's session) and the attachments.
func mergeRefs(images []protocol.ImageRef, atts []protocol.AttachmentRef) []core.AttachmentRef {
	out := imagesOf(images)
	seen := map[string]bool{}
	for _, a := range out {
		seen[a.ID] = true
	}
	for _, a := range atts {
		if seen[a.ID] {
			continue
		}
		seen[a.ID] = true
		r := core.AttachmentRef{ID: a.ID, Name: a.Name, ContentType: a.ContentType, Size: a.Size}
		if a.SHA256 != nil {
			r.SHA256 = *a.SHA256
		}
		out = append(out, r)
	}
	return dedupeRefs(out)
}

// dedupeRefs keeps the first of each id, at most core.MaxToolAttachments.
func dedupeRefs(list []core.AttachmentRef) []core.AttachmentRef {
	out := make([]core.AttachmentRef, 0, len(list))
	seen := map[string]bool{}
	for _, a := range list {
		if seen[a.ID] {
			continue
		}
		seen[a.ID] = true
		out = append(out, a)
	}
	if len(out) > core.MaxToolAttachments {
		out = out[:core.MaxToolAttachments]
	}
	return out
}

// fitted is a delivery's attachments in the form its node takes.
type fitted struct {
	text   string
	images []protocol.ImageRef
	atts   []protocol.AttachmentRef
}

// fitAttachments decides how the attachments reach the node: a daemon with attachments_v2
// gets them all as attachments (it downloads them and lists their paths); an older one gets the
// images and PDFs up to 5 MB as images, text files over maxInlineText inlined cut, and a line
// for the rest asking to upgrade the daemon (legacyFit).
func (o *Orchestrator) fitAttachments(ctx context.Context, tx *core.Tx, nodeID, projectID, text string, refs []core.AttachmentRef) (fitted, error) {
	if len(refs) == 0 {
		return fitted{text: text}, nil
	}
	v2, err := core.NodeHasFeature(ctx, tx, nodeID, FeatureAttachmentsV2)
	if err != nil {
		return fitted{}, err
	}
	if v2 {
		return fitted{text: text, atts: attachmentRefs(refs)}, nil
	}
	return o.legacyFit(ctx, tx, projectID, text, refs)
}

// legacyFit is fitAttachments for a daemon without attachments_v2. When an attachment does not
// get through whole (cut text, other types), the text ends with an [附件] block naming every
// attachment (id, name, type, size) and what became of it, as the new daemons' block does; when
// all do (images and PDFs up to 5 MB, small text files) the message is what it was before #58.
func (o *Orchestrator) legacyFit(ctx context.Context, tx *core.Tx, projectID, text string, refs []core.AttachmentRef) (fitted, error) {
	var long []string
	for _, r := range refs {
		if core.IsInlineText(r.ContentType) && r.Size > maxInlineText {
			long = append(long, r.ID)
		}
	}
	texts, err := core.AttachmentTexts(ctx, tx, o.core.Blobs, projectID, long)
	if err != nil {
		o.log.Warn("read text attachments for an old daemon", "err", err)
		texts = map[string][]byte{}
	}
	out := fitted{}
	var b, inline strings.Builder
	lost := false
	for _, r := range refs {
		head := fmt.Sprintf("- %s %s（%s，%s）", r.ID, r.Name, r.ContentType, sizeText(int64(r.Size)))
		switch {
		case legacyImageTypes[r.ContentType] && r.Size <= legacyImageBytes && len(out.images) < maxLegacyImages:
			name, size := r.Name, r.Size
			out.images = append(out.images, protocol.ImageRef{ID: r.ID, MediaType: protocol.ImageRefMediaType(r.ContentType), Name: &name, Size: &size})
			fmt.Fprintf(&b, "\n%s：已随消息附上", head)
		case core.IsInlineText(r.ContentType) && r.Size <= maxInlineText:
			fmt.Fprintf(&b, "\n%s：内容已放在正文里", head)
		case core.IsInlineText(r.ContentType) && texts[r.ID] != nil:
			body, cut := clipText(string(texts[r.ID]), maxInlineText)
			fmt.Fprintf(&inline, "\n\n[附件：%s%s]\n```\n%s\n```", r.Name, cut, body)
			fmt.Fprintf(&b, "\n%s：内容已放在正文里（节点版本过旧，只放了开头部分；升级节点守护进程后可以拿到完整文件）", head)
			lost = true
		default:
			lost = true
			fmt.Fprintf(&b, "\n%s：无法交给这个节点：节点守护进程版本过旧，请升级后再发", head)
		}
	}
	out.text = text + inline.String()
	if lost {
		// Only when something did not get through as before #58: then the AI learns every id.
		out.text += "\n\n[附件]" + b.String()
	}
	return out, nil
}

// fitFirst rewrites a first message for the node it opens on (fitAttachments).
func (o *Orchestrator) fitFirst(ctx context.Context, tx *core.Tx, nodeID, projectID string, first *protocol.FirstMessage) (*protocol.FirstMessage, error) {
	if first == nil || (len(first.Images) == 0 && len(first.Attachments) == 0) {
		return first, nil
	}
	f, err := o.fitAttachments(ctx, tx, nodeID, projectID, first.Text, mergeRefs(first.Images, first.Attachments))
	if err != nil {
		return nil, err
	}
	out := *first
	out.Text, out.Images, out.Attachments = f.text, f.images, f.atts
	return &out, nil
}

// joinRefs appends more attachments to a first message folded from several (holds, handoffs,
// moves): every attachment once, as attachments (fitFirst decides per node later).
func joinRefs(first *protocol.FirstMessage, more protocol.FirstMessage) {
	all := mergeRefs(first.Images, first.Attachments)
	all = append(all, mergeRefs(more.Images, more.Attachments)...)
	first.Images, first.Attachments = nil, attachmentRefs(dedupeRefs(all))
}

// sizeText prints a byte count as B / KB / MB.
func sizeText(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// AttachmentList names attachments for a stored text (mail, an answer): id, name, type and size,
// so whoever reads it later can fetch them with read_attachment ("" without attachments).
func AttachmentList(refs []core.AttachmentRef) string {
	if len(refs) == 0 {
		return ""
	}
	parts := make([]string, len(refs))
	for i, r := range refs {
		parts[i] = fmt.Sprintf("%s %s（%s，%s）", r.ID, r.Name, r.ContentType, sizeText(int64(r.Size)))
	}
	return "\n（附件：" + strings.Join(parts, "；") + "。用 read_attachment(id) 取到工作目录）"
}

// DeliveryText is the text delivered with attachments: their text files up to maxInlineText
// bytes inlined (bigger ones only travel as files; legacyFit inlines them cut for old daemons).
func (o *Orchestrator) DeliveryText(ctx context.Context, tx *core.Tx, projectID, text string, refs []core.AttachmentRef) (string, error) {
	out, _, err := o.inlineTexts(ctx, tx, core.Conversation{ProjectID: &projectID}, text, refs)
	return out, err
}
