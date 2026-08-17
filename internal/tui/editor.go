package tuiapp

import (
	"context"
	"fmt"
	"strings"
	"time"

	tui "github.com/grindlemire/go-tui"

	"github.com/timborovkov/posthouse/internal/model"
	"github.com/timborovkov/posthouse/internal/safeio"
	"github.com/timborovkov/posthouse/internal/service"
)

const (
	mailPageSize   = 25
	eventPageSize  = 100
	rfc3339Example = "2026-08-17T09:00:00Z"
)

func (p *posthouseApp) modalKeyMap() tui.KeyMap {
	// Bindings are installed once when the modal mounts, so handlers must
	// branch on current editor state instead of returning different maps.
	return tui.KeyMap{
		tui.OnPreemptStop(tui.KeyEnter, func(ke tui.KeyEvent) {
			if !p.editor.Get() {
				p.confirmModal(ke)
			}
		}),
		tui.OnPreemptStop(tui.KeyEscape, func(ke tui.KeyEvent) {
			if p.editor.Get() {
				p.cancelEditor()
				return
			}
			p.cancelModal()
		}),
		tui.OnPreemptStop(tui.KeyCtrlS, func(ke tui.KeyEvent) {
			if p.editor.Get() {
				p.submitEditor()
			}
		}),
		tui.OnPreemptStop(tui.Rune('s'), func(ke tui.KeyEvent) {
			if !p.editor.Get() {
				p.beginAttachmentSave()
			}
		}),
		tui.OnPreemptStop(tui.Rune('q'), func(ke tui.KeyEvent) { p.cancel(); ke.App().Stop() }),
	}
}

func (p *posthouseApp) confirmModalAction()     { p.confirmModal(tui.KeyEvent{}) }
func (p *posthouseApp) submitEditorText(string) { p.submitEditor() }

func (p *posthouseApp) editorLabels() []string {
	switch p.editorKind.Get() {
	case "connection":
		return []string{"ID", "Name", "Category", "Identity email", "Mail username", "Mail secret env", "IMAP TLS address", "SMTP TLS address", "CalDAV URL", "CalDAV username", "CalDAV secret env"}
	case "action":
		return []string{"Action", "Recipient / destination", "Body type text/html", "Body"}
	case "event-action":
		return []string{"Action", "Title", "Start RFC3339", "End RFC3339"}
	case "mail":
		return []string{"Connection", "Mode send/draft", "To", "CC", "BCC", "Subject", "Body type text/html", "Body", "Attachment paths"}
	case "save":
		return []string{"Save path"}
	default:
		return []string{"Connection", "Collection", "Title", "Start RFC3339", "End RFC3339"}
	}
}

func (p *posthouseApp) editorTitle() string {
	switch p.editorKind.Get() {
	case "connection":
		return "Onboard protocol connection"
	case "action":
		return "Message action: reply, forward, mark-read, mark-unread, flag, unflag, move, archive, trash"
	case "event-action":
		return "Event action: update, update-series, delete"
	case "mail":
		return "Compose mail or provider draft"
	case "save":
		return "Save attachment"
	default:
		return "Create CalDAV event"
	}
}

func (p *posthouseApp) editorHelp() string {
	if p.editorKind.Get() == "save" {
		return "Tab fields · Enter saves · Esc cancels"
	}
	if p.editorHasTimeFields() {
		return "Tab fields · Enter prepares · Ctrl+S prepares · Esc cancels · RFC3339 e.g. " + rfc3339Example
	}
	if p.editorHasBody() {
		return "Tab fields · Enter newline in body · Ctrl+S prepares · Esc cancels"
	}
	return "Tab fields · Enter prepares · Esc cancels"
}

func (p *posthouseApp) editorHasBody() bool {
	kind := p.editorKind.Get()
	return kind == "mail" || kind == "action"
}

func (p *posthouseApp) editorHasTimeFields() bool {
	kind := p.editorKind.Get()
	return kind == "event" || kind == "event-action"
}

func (p *posthouseApp) editorFieldIsBody(index int) bool {
	switch p.editorKind.Get() {
	case "mail":
		return index == 7
	case "action":
		return index == 3
	default:
		return false
	}
}

func (p *posthouseApp) editorFieldIsTime(index int) bool {
	switch p.editorKind.Get() {
	case "event":
		return index == 3 || index == 4
	case "event-action":
		return index == 2 || index == 3
	default:
		return false
	}
}

func (p *posthouseApp) editorPlaceholder(index int) string {
	if p.editorFieldIsTime(index) {
		return rfc3339Example
	}
	if p.editorKind.Get() == "mail" && index == 6 {
		return "text"
	}
	if p.editorKind.Get() == "action" && index == 2 {
		return "text"
	}
	return ""
}

func (p *posthouseApp) editorFieldKey(index int) string {
	return fmt.Sprintf("%s-%d-%d", p.editorKind.Get(), index, p.editorTick.Get())
}

func (p *posthouseApp) rfc3339Mark(index int) string {
	if p.rfc3339Error(index) != "" {
		return "×"
	}
	if !p.editorFieldIsTime(index) || index >= len(p.editorFields) {
		return ""
	}
	if strings.TrimSpace(p.editorFields[index].Get()) == "" {
		return ""
	}
	return "✓"
}

func (p *posthouseApp) rfc3339Error(index int) string {
	if !p.editorFieldIsTime(index) || index >= len(p.editorFields) {
		return ""
	}
	value := strings.TrimSpace(p.editorFields[index].Get())
	if value == "" {
		return ""
	}
	if _, err := time.Parse(time.RFC3339, value); err != nil {
		return fmt.Sprintf("invalid RFC3339, e.g. %s", rfc3339Example)
	}
	return ""
}

func (p *posthouseApp) editorValue(index int) string {
	if index < 0 || index >= len(p.editorFields) {
		return ""
	}
	return p.editorFields[index].Get()
}

func (p *posthouseApp) editorValues() []string {
	values := make([]string, len(p.editorFields))
	for index, field := range p.editorFields {
		values[index] = field.Get()
	}
	return values
}

func (p *posthouseApp) beginEditor(kind string, values []string) {
	fields := make([]*tui.State[string], len(values))
	for index, value := range values {
		field := tui.NewState(value)
		if p.app != nil {
			field.BindApp(p.app)
		}
		fields[index] = field
	}
	p.editorKind.Set(kind)
	p.editorFields = fields
	p.editor.Set(true)
	p.modal.Set(true)
	p.pendingToken.Set("")
	p.pendingDiscover.Set("")
	p.errorText.Set("")
	p.editorTick.Set(p.editorTick.Get() + 1)
}

func (p *posthouseApp) cancelEditor() {
	p.editor.Set(false)
	p.modal.Set(false)
	p.editorFields = nil
	p.editorKind.Set("")
}

func (p *posthouseApp) composeBodies(bodyType, body string) (string, string, error) {
	switch strings.ToLower(strings.TrimSpace(bodyType)) {
	case "", "text", "plain":
		return body, "", nil
	case "html":
		return "", body, nil
	default:
		return "", "", fmt.Errorf("body type must be text or html")
	}
}

func (p *posthouseApp) submitEditor() {
	if !p.editor.Get() {
		return
	}
	values := p.editorValues()
	var prepared model.PreparedOperation
	var err error
	switch p.editorKind.Get() {
	case "connection":
		if len(values) != 11 || values[0] == "" || values[1] == "" {
			err = fmt.Errorf("connection ID and name are required")
		} else {
			connection := model.Connection{ID: values[0], Name: values[1], Category: values[2], Identity: model.Identity{Email: values[3]}}
			if values[6] != "" || values[7] != "" {
				connection.Mail = &model.MailConfig{Username: values[4], Secret: model.SecretRef{Env: values[5]}, IMAP: model.IMAPConfig{Address: values[6], TLS: values[6] != ""}, SMTP: model.SMTPConfig{Address: values[7], TLS: values[7] != ""}, SentCopy: "provider-managed"}
			}
			if values[8] != "" {
				connection.Calendar = &model.CalendarConfig{Kind: "caldav", URL: values[8], Username: values[9], Secret: model.SecretRef{Env: values[10]}}
			}
			err = p.service.UpsertConnection(connection, false)
			if err == nil {
				p.editor.Set(false)
				p.editorFields = nil
				p.errorText.Set("")
				p.pendingDiscover.Set(connection.ID)
				p.modalText.Set("Connection saved\n\nEnter discovers folders and calendars · Esc skips")
				p.refresh()
				return
			}
		}
	case "action":
		item, ok := p.selectedMessage()
		if !ok {
			err = fmt.Errorf("selected message is no longer available")
		} else if len(values) != 4 {
			err = fmt.Errorf("message action is incomplete")
		} else {
			action := strings.ToLower(strings.TrimSpace(values[0]))
			payload := service.MailAction{Folder: item.Folder, UID: item.UID}
			kind := "mail." + action
			text, htmlBody := "", ""
			if action == "reply" || action == "forward" {
				text, htmlBody, err = p.composeBodies(values[2], values[3])
			}
			if err == nil {
				switch action {
				case "reply":
					prepared, err = p.service.PrepareReply(p.ctx, item.ConnectionID, item.Folder, item.UID, text, htmlBody)
				case "forward":
					prepared, err = p.service.PrepareForward(p.ctx, item.ConnectionID, item.Folder, item.UID, splitValues(values[1]), text, htmlBody)
				case "mark-read":
					value := true
					payload.Seen = &value
					kind = "mail.mark"
				case "mark-unread":
					value := false
					payload.Seen = &value
					kind = "mail.mark"
				case "flag":
					value := true
					payload.Flagged = &value
					kind = "mail.mark"
				case "unflag":
					value := false
					payload.Flagged = &value
					kind = "mail.mark"
				case "move":
					payload.Destination = values[1]
				case "archive", "trash":
				default:
					err = fmt.Errorf("unknown action %q", action)
				}
			}
			if err == nil && action != "reply" && action != "forward" {
				prepared, err = p.service.PrepareMailAction(p.ctx, item.ConnectionID, kind, payload)
			}
		}
	case "event-action":
		items := p.events.Get()
		if len(items) == 0 || p.selected.Get() >= len(items) {
			err = fmt.Errorf("selected event is no longer available")
		} else {
			event := items[p.selected.Get()]
			action := strings.ToLower(strings.TrimSpace(values[0]))
			if action == "delete" {
				prepared, err = p.service.PrepareCalendarDelete(p.ctx, event.ConnectionID, event.CollectionID, event.Href, event.ETag, event.RecurrenceID)
			} else if action == "update" || action == "update-series" {
				if action == "update-series" && event.RecurrenceID != "" {
					err = fmt.Errorf("cannot replace a recurring series from an expanded occurrence; refresh and edit the series master")
				} else {
					var start, end time.Time
					start, err = parseRequiredRFC3339("start", values[2])
					if err == nil {
						end, err = parseRequiredRFC3339("end", values[3])
					}
					if err == nil && !end.After(start) {
						err = fmt.Errorf("end must be after start")
					}
					if err == nil {
						event.Title = values[1]
						event.Start = start
						event.End = end
						prepared, err = p.service.PrepareCalendarWrite(p.ctx, event.ConnectionID, "calendar.update", event)
					}
				}
			} else {
				err = fmt.Errorf("unknown event action %q", action)
			}
		}
	case "mail":
		if len(values) != 9 || values[0] == "" {
			err = fmt.Errorf("connection is required")
		} else {
			text, htmlBody, bodyErr := p.composeBodies(values[6], values[7])
			err = bodyErr
			if err == nil {
				message := model.SendMessage{ConnectionID: values[0], To: splitValues(values[2]), CC: splitValues(values[3]), BCC: splitValues(values[4]), Subject: values[5], Text: text, HTML: htmlBody}
				for _, path := range splitValues(values[8]) {
					message.Attachments = append(message.Attachments, model.AttachmentInput{Path: path})
				}
				mode := strings.ToLower(strings.TrimSpace(values[1]))
				if mode == "draft" {
					prepared, err = p.service.PrepareDraft(p.ctx, values[0], "mail.draft.create", "", 0, message)
				} else if mode == "send" || mode == "" {
					prepared, err = p.service.PrepareSend(p.ctx, message)
				} else {
					err = fmt.Errorf("mode must be send or draft")
				}
			}
		}
	case "save":
		if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
			err = fmt.Errorf("save path is required")
		} else if len(p.attachmentData.Get()) == 0 {
			err = fmt.Errorf("no attachment is loaded")
		} else {
			path, writeErr := safeio.WriteFile(strings.TrimSpace(values[0]), p.attachmentData.Get(), false)
			if writeErr != nil {
				err = writeErr
				if strings.Contains(writeErr.Error(), "already exists") {
					err = fmt.Errorf("%s already exists; pick another path", strings.TrimSpace(values[0]))
				}
			} else {
				p.editor.Set(false)
				p.editorFields = nil
				p.errorText.Set("")
				p.modalText.Set("Attachment saved\n\n" + path)
				return
			}
		}
	default:
		if len(values) != 5 || values[0] == "" || values[1] == "" || values[2] == "" {
			err = fmt.Errorf("connection, collection, and title are required")
		} else {
			var start, end time.Time
			start, err = parseRequiredRFC3339("start", values[3])
			if err == nil {
				end, err = parseRequiredRFC3339("end", values[4])
			}
			if err == nil && !end.After(start) {
				err = fmt.Errorf("end must be after start")
			}
			if err == nil {
				prepared, err = p.service.PrepareCalendarWrite(p.ctx, values[0], "calendar.create", model.Event{ID: fmt.Sprintf("posthouse-%d", time.Now().UnixNano()), CollectionID: values[1], Title: values[2], Start: start, End: end})
			}
		}
	}
	if err != nil {
		p.errorText.Set(err.Error())
		return
	}
	p.editor.Set(false)
	p.editorFields = nil
	p.errorText.Set("")
	p.pendingToken.Set(prepared.Token)
	p.modalText.Set(formatPreview(prepared))
}

func parseRequiredRFC3339(name, value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, fmt.Errorf("%s is required RFC3339, e.g. %s", name, rfc3339Example)
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s must be RFC3339, e.g. %s", name, rfc3339Example)
	}
	return parsed, nil
}

func formatDiscover(connection model.Connection) string {
	lines := []string{"Discovered connection " + connection.ID}
	if connection.Mail != nil {
		folders := connection.Mail.Folders
		lines = append(lines, fmt.Sprintf("Folders  inbox=%s sent=%s drafts=%s archive=%s trash=%s junk=%s", folders.Inbox, folders.Sent, folders.Drafts, folders.Archive, folders.Trash, folders.Junk))
	}
	if connection.Calendar != nil {
		if len(connection.Calendar.Collections) == 0 {
			lines = append(lines, "Calendars  none")
		}
		for _, collection := range connection.Calendar.Collections {
			lines = append(lines, fmt.Sprintf("Calendar %-12s %s", collection.ID, collection.Name))
		}
	}
	return strings.Join(lines, "\n")
}

func moreMarker(next string, count int) string {
	if next == "" {
		return fmt.Sprintf("%d", count)
	}
	return fmt.Sprintf("%d · more", count)
}

func (p *posthouseApp) pageList(delta int) {
	switch p.view.Get() {
	case 1:
		if delta > 0 {
			if p.mailNext.Get() == "" {
				return
			}
			p.mailHistory.Set(append(append([]string{}, p.mailHistory.Get()...), p.mailCursor.Get()))
			p.mailCursor.Set(p.mailNext.Get())
			p.mailNext.Set("")
		} else {
			history := p.mailHistory.Get()
			if len(history) == 0 {
				return
			}
			p.mailCursor.Set(history[len(history)-1])
			p.mailHistory.Set(history[:len(history)-1])
			p.mailNext.Set("")
		}
		p.refreshScope("mail")
	case 3:
		if delta > 0 {
			if p.eventNext.Get() == "" {
				return
			}
			p.eventHistory.Set(append(append([]string{}, p.eventHistory.Get()...), p.eventCursor.Get()))
			p.eventCursor.Set(p.eventNext.Get())
			p.eventNext.Set("")
		} else {
			history := p.eventHistory.Get()
			if len(history) == 0 {
				return
			}
			p.eventCursor.Set(history[len(history)-1])
			p.eventHistory.Set(history[:len(history)-1])
			p.eventNext.Set("")
		}
		p.refreshScope("events")
	}
}

func (p *posthouseApp) resetMailPaging() {
	p.mailCursor.Set("")
	p.mailNext.Set("")
	p.mailHistory.Set(nil)
}

func (p *posthouseApp) resetEventPaging() {
	p.eventCursor.Set("")
	p.eventNext.Set("")
	p.eventHistory.Set(nil)
}

func (p *posthouseApp) discoverSelected() {
	if p.view.Get() != 0 {
		return
	}
	items := p.connections.Get()
	if len(items) == 0 {
		return
	}
	p.startDiscover(items[p.selected.Get()].ID)
}

func (p *posthouseApp) startDiscover(id string) {
	p.pendingDiscover.Set("")
	p.startProviderRead("discover", func(ctx context.Context) providerReadSnapshot {
		connection, err := p.discoverConnection(ctx, id)
		return providerReadSnapshot{connection: connection, err: err}
	})
}

func attachmentSaveName(attachment model.Attachment) string {
	if attachment.Name != "" {
		return attachment.Name
	}
	return "attachment"
}

func (p *posthouseApp) beginAttachmentSave() {
	if p.view.Get() == 2 {
		detail := p.detail.Get()
		attachments := detail.Attachments
		if len(attachments) == 0 || p.selected.Get() >= len(attachments) {
			return
		}
		item := attachments[p.selected.Get()]
		loaded := p.attachment.Get()
		if loaded.ID == item.ID && len(p.attachmentData.Get()) > 0 {
			p.beginEditor("save", []string{attachmentSaveName(item)})
			return
		}
		p.startProviderRead("attachment-save", func(ctx context.Context) providerReadSnapshot {
			metadata, data, err := p.getAttachment(ctx, detail.ConnectionID, detail.Folder, detail.UID, item.ID)
			return providerReadSnapshot{attachment: metadata, data: data, err: err}
		})
		return
	}
	if !p.modal.Get() || len(p.attachmentData.Get()) == 0 {
		return
	}
	p.beginEditor("save", []string{attachmentSaveName(p.attachment.Get())})
}
