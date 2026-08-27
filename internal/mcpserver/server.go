package mcpserver

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/timborovkov/posthouse/internal/calendar"
	"github.com/timborovkov/posthouse/internal/httpauth"
	postmail "github.com/timborovkov/posthouse/internal/mail"
	"github.com/timborovkov/posthouse/internal/model"
	"github.com/timborovkov/posthouse/internal/policy"
	"github.com/timborovkov/posthouse/internal/service"
	"github.com/timborovkov/posthouse/internal/state"
)

const (
	Version                = "0.2.0"
	maxMCPHTTPRequestBytes = 36 << 20
)

type Server struct {
	service *service.Service
	mcp     *mcp.Server
	profile string
}

// New builds an MCP server. profileOverride may be "", "full", or "readonly".
// Empty uses config policy.mcp_profile, then POSTHOUSE_MCP_PROFILE, then full.
func New(application *service.Service, profileOverride string) (*Server, error) {
	cfg, err := application.RawPolicy()
	if err != nil {
		return nil, err
	}
	resolved, err := policy.MCPProfile(cfg, profileOverride)
	if err != nil {
		return nil, err
	}
	server := &Server{service: application, profile: resolved}
	server.mcp = mcp.NewServer(&mcp.Implementation{Name: "posthouse", Version: Version}, nil)
	server.registerTools()
	return server, nil
}

func (s *Server) RunStdio(ctx context.Context) error {
	return s.mcp.Run(ctx, &mcp.StdioTransport{})
}

func (s *Server) RunHTTP(ctx context.Context, address string, token string, allowContainerListener bool, logger *slog.Logger) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid HTTP address %q: %w", address, err)
	}
	if !isLoopback(host) && !allowContainerListener {
		return fmt.Errorf("direct HTTP must listen on loopback; use a TLS-terminating reverse proxy for remote access")
	}
	handler, err := s.HTTPHandler(token, logger)
	if err != nil {
		return err
	}
	httpServer := &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 2 * time.Minute}
	logger.Info("Posthouse HTTP listening", "address", address, "mcp", "/mcp", "rest", "/v1")
	return serveHTTPUntilShutdown(ctx, httpServer.ListenAndServe, httpServer.Shutdown)
}

func (s *Server) HTTPHandler(token string, logger *slog.Logger) (http.Handler, error) {
	guard, err := httpauth.NewGuard(token)
	if err != nil {
		return nil, err
	}
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.mcp }, &mcp.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true, Logger: logger, SessionTimeout: 5 * time.Minute,
	})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("GET /readyz", func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if err := s.service.Ready(request.Context()); err != nil {
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = writer.Write([]byte(`{"status":"not_ready"}`))
			return
		}
		_, _ = writer.Write([]byte(`{"status":"ready"}`))
	})
	protected := http.NewServeMux()
	protected.Handle("/mcp", http.MaxBytesHandler(mcpHandler, maxMCPHTTPRequestBytes))
	s.registerREST(protected)
	mux.Handle("/", guard.Middleware(http.MaxBytesHandler(protected, maxMCPHTTPRequestBytes)))
	return mux, nil
}

func serveHTTPUntilShutdown(ctx context.Context, serve func() error, shutdown func(context.Context) error) error {
	stopWatcher := make(chan struct{})
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		select {
		case <-ctx.Done():
			shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = shutdown(shutdownContext)
		case <-stopWatcher:
		}
	}()
	serveErr := serve()
	close(stopWatcher)
	<-shutdownDone
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	return nil
}

type selectorInput struct {
	Connections []string `json:"connections,omitempty" jsonschema:"connection IDs or names to include"`
	Category    string   `json:"category,omitempty" jsonschema:"single category to include"`
	Labels      []string `json:"labels,omitempty" jsonschema:"all labels that selected connections must have"`
	Collections []string `json:"collections,omitempty" jsonschema:"calendar collection IDs or names to include"`
	Capability  string   `json:"capability,omitempty" jsonschema:"granular capability such as mail.read or calendar.write"`
}

type pageInput struct {
	PageSize int    `json:"page_size,omitempty" jsonschema:"number of items to return; each tool documents its maximum"`
	Cursor   string `json:"cursor,omitempty" jsonschema:"opaque next_cursor returned by the previous call with identical filters"`
}

type connectionsListInput struct {
	selectorInput
	pageInput
}

func (input selectorInput) selector() model.Selector {
	return model.Selector{Connections: input.Connections, Category: input.Category, Labels: input.Labels, Collections: input.Collections, Capability: input.Capability}
}

type messageSearchInput struct {
	selectorInput
	pageInput
	Folder string `json:"folder,omitempty"`
	Query  string `json:"query,omitempty" jsonschema:"text to search in message headers and bodies"`
	Since  string `json:"since,omitempty" jsonschema:"inclusive RFC3339 timestamp"`
	Before string `json:"before,omitempty" jsonschema:"exclusive RFC3339 timestamp"`
	Unread bool   `json:"unread,omitempty"`
	Mode   string `json:"mode,omitempty" jsonschema:"empty for live-first stale fallback, offline for cache-only, or refresh for live-only"`
}

type unreadCountsInput struct {
	selectorInput
	Folder string `json:"folder,omitempty"`
}

type sendMessageInput struct {
	Connection  string               `json:"connection" jsonschema:"exact connection ID or unique name"`
	To          []string             `json:"to"`
	CC          []string             `json:"cc,omitempty"`
	BCC         []string             `json:"bcc,omitempty"`
	Subject     string               `json:"subject"`
	Text        string               `json:"text,omitempty"`
	HTML        string               `json:"html,omitempty" jsonschema:"HTML body; sent as text/html or multipart/alternative with text"`
	ReplyTo     string               `json:"reply_to,omitempty"`
	InReplyTo   string               `json:"in_reply_to,omitempty"`
	References  []string             `json:"references,omitempty"`
	Attachments []mcpAttachmentInput `json:"attachments,omitempty"`
}

type mcpAttachmentInput struct {
	Name        string `json:"name"`
	ContentType string `json:"content_type,omitempty"`
	Data        []byte `json:"data" jsonschema:"base64-encoded attachment bytes; all attachments in one operation are limited to 25 MiB total"`
}

func mcpAttachments(inputs []mcpAttachmentInput) []model.AttachmentInput {
	result := make([]model.AttachmentInput, len(inputs))
	for index, input := range inputs {
		result[index] = model.AttachmentInput{Name: input.Name, ContentType: input.ContentType, Data: input.Data}
	}
	return result
}

type messageReplyInput struct {
	Connection string `json:"connection"`
	ID         string `json:"id,omitempty" jsonschema:"opaque message id from messages_search; preferred over uid"`
	Folder     string `json:"folder,omitempty"`
	UID        uint32 `json:"uid,omitempty" jsonschema:"deprecated IMAP-only alias for id"`
	Text       string `json:"text,omitempty"`
	HTML       string `json:"html,omitempty" jsonschema:"HTML body to place before the quoted message"`
}

type messageForwardInput struct {
	messageReplyInput
	To       []string `json:"to"`
	Verbatim bool     `json:"verbatim,omitempty" jsonschema:"when true, forward original parts as attachments; original body is omitted from the preview"`
}

type messageDraftInput struct {
	Connection string          `json:"connection"`
	Action     string          `json:"action" jsonschema:"create, update, or delete"`
	ID         string          `json:"id,omitempty" jsonschema:"opaque draft id from messages_search; required for update and delete"`
	Folder     string          `json:"folder,omitempty"`
	UID        uint32          `json:"uid,omitempty" jsonschema:"deprecated IMAP-only alias for id"`
	Message    mcpDraftMessage `json:"message,omitempty"`
}

type mcpDraftMessage struct {
	To          []string             `json:"to,omitempty"`
	CC          []string             `json:"cc,omitempty"`
	BCC         []string             `json:"bcc,omitempty"`
	Subject     string               `json:"subject,omitempty"`
	Text        string               `json:"text,omitempty"`
	HTML        string               `json:"html,omitempty"`
	ReplyTo     string               `json:"reply_to,omitempty"`
	InReplyTo   string               `json:"in_reply_to,omitempty"`
	References  []string             `json:"references,omitempty"`
	Attachments []mcpAttachmentInput `json:"attachments,omitempty"`
}

func (input mcpDraftMessage) model() model.SendMessage {
	return model.SendMessage{To: input.To, CC: input.CC, BCC: input.BCC, Subject: input.Subject, Text: input.Text, HTML: input.HTML, ReplyTo: input.ReplyTo, InReplyTo: input.InReplyTo, References: input.References, Attachments: mcpAttachments(input.Attachments)}
}

func (input messageReplyInput) locator() service.MessageLocator {
	return service.MessageLocator{ID: input.ID, Folder: input.Folder, UID: input.UID}
}

func (input messageDraftInput) locator() service.MessageLocator {
	return service.MessageLocator{ID: input.ID, Folder: input.Folder, UID: input.UID}
}

func (input messageGetInput) locator() service.MessageLocator {
	return service.MessageLocator{ID: input.ID, Folder: input.Folder, UID: input.UID}
}

func (input attachmentGetInput) locator() service.MessageLocator {
	return service.MessageLocator{ID: input.ID, Folder: input.Folder, UID: input.UID}
}

type operationOutput struct {
	OK bool `json:"ok"`
}

type operationInput struct {
	Token string `json:"token"`
}

type messageGetInput struct {
	Connection string `json:"connection"`
	ID         string `json:"id,omitempty" jsonschema:"opaque message id from messages_search; preferred over uid"`
	Folder     string `json:"folder,omitempty"`
	UID        uint32 `json:"uid,omitempty" jsonschema:"deprecated IMAP-only alias for id"`
	Mode       string `json:"mode,omitempty" jsonschema:"empty for live-first stale fallback, offline for cache-only, or refresh for live-only"`
}

type attachmentGetInput struct {
	messageGetInput
	AttachmentID string `json:"attachment_id"`
	Offset       int    `json:"offset,omitempty"`
	Limit        int    `json:"limit,omitempty"`
	Cursor       string `json:"cursor,omitempty" jsonschema:"opaque attachment snapshot cursor returned with next_offset"`
	ExtractText  bool   `json:"extract_text,omitempty" jsonschema:"when true and the attachment is PDF, return extracted plain text instead of raw bytes"`
}

type attachmentChunkOutput struct {
	Attachment model.Attachment `json:"attachment"`
	Offset     int              `json:"offset"`
	NextOffset int              `json:"next_offset,omitempty"`
	NextCursor string           `json:"next_cursor,omitempty"`
	DataBase64 string           `json:"data_base64"`
	Text       string           `json:"text,omitempty"`
}

type messageActionInput struct {
	Connection  string   `json:"connection"`
	Action      string   `json:"action" jsonschema:"mark, move, archive, trash, or junk"`
	ID          string   `json:"id,omitempty" jsonschema:"opaque message id from messages_search; preferred over uid"`
	Folder      string   `json:"folder,omitempty"`
	UID         uint32   `json:"uid,omitempty" jsonschema:"deprecated IMAP-only alias for id"`
	UIDs        []uint32 `json:"uids,omitempty" jsonschema:"batch IMAP UIDs; when set, id/uid are ignored"`
	Destination string   `json:"destination,omitempty"`
	Seen        *bool    `json:"seen,omitempty"`
	Flagged     *bool    `json:"flagged,omitempty"`
}

type connectionInput struct {
	Connection string `json:"connection"`
}

type eventMutationInput struct {
	Connection string      `json:"connection"`
	Event      model.Event `json:"event"`
}

type eventDeleteInput struct {
	Connection   string `json:"connection"`
	Collection   string `json:"collection"`
	Href         string `json:"href"`
	ETag         string `json:"etag"`
	RecurrenceID string `json:"recurrence_id,omitempty" jsonschema:"recurrence_id from events_list; expanded occurrences cannot be deleted as whole objects"`
}

type eventListInput struct {
	selectorInput
	pageInput
	Start string `json:"start,omitempty" jsonschema:"inclusive RFC3339 timestamp"`
	End   string `json:"end,omitempty" jsonschema:"exclusive RFC3339 timestamp"`
	Query string `json:"query,omitempty" jsonschema:"text to find in title description or location"`
	Mode  string `json:"mode,omitempty" jsonschema:"empty for live-first stale fallback, offline for cache-only, or refresh for live-only"`
}

type eventICSInput struct {
	ID          string   `json:"id,omitempty"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Location    string   `json:"location,omitempty"`
	Start       string   `json:"start" jsonschema:"RFC3339 timestamp"`
	End         string   `json:"end" jsonschema:"RFC3339 timestamp"`
	AllDay      bool     `json:"all_day,omitempty"`
	Attendees   []string `json:"attendees,omitempty"`
	Organizer   string   `json:"organizer,omitempty"`
	Method      string   `json:"method,omitempty" jsonschema:"empty for a portable VEVENT, request or cancel for METHOD invitations"`
	Sequence    int      `json:"sequence,omitempty"`
}

type icsOutput struct {
	Event    model.Event `json:"event"`
	Filename string      `json:"filename"`
	MIMEType string      `json:"mime_type"`
	ICS      string      `json:"ics"`
}

func (s *Server) registerTools() {
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}
	openWorld := true
	nondestructive := false
	destructive := true
	executeWrite := &mcp.ToolAnnotations{DestructiveHint: &destructive, IdempotentHint: true, OpenWorldHint: &openWorld}

	mcp.AddTool(s.mcp, &mcp.Tool{Name: "connections_list", Title: "List connections", Description: "List configured mail and calendar connections by name, category, and labels, up to 200 per page. Pass next_cursor back unchanged with identical filters. Secret values and secret environment-variable names are never returned.", Annotations: readOnly},
		func(_ context.Context, _ *mcp.CallToolRequest, input connectionsListInput) (*mcp.CallToolResult, model.ConnectionPage, error) {
			page, err := s.service.ListConnections(input.selector(), input.PageSize, input.Cursor)
			return nil, page, err
		})

	mcp.AddTool(s.mcp, &mcp.Tool{Name: "messages_search", Title: "Search messages", Description: "List or search messages across selected mail connections, including generic IMAP and operator-authorized OAuth connections, up to 100 per page. Each message has an opaque id plus connection_id; folder is mailbox metadata. Pass next_cursor back unchanged with identical filters. Query language stays generic; do not pass provider search syntax. Offline full-text fallback searches available encrypted cached headers and bodies and returns an offline_search_incomplete source warning when uncached content may be omitted. Prefer messages_triage for inbox cleanup workflows.", Annotations: readOnly},
		func(ctx context.Context, _ *mcp.CallToolRequest, input messageSearchInput) (*mcp.CallToolResult, model.MessagePage, error) {
			since, err := optionalTime(input.Since)
			if err != nil {
				return nil, model.MessagePage{}, fmt.Errorf("since: %w", err)
			}
			before, err := optionalTime(input.Before)
			if err != nil {
				return nil, model.MessagePage{}, fmt.Errorf("before: %w", err)
			}
			if input.Mode != "" && input.Mode != "offline" && input.Mode != "refresh" {
				return nil, model.MessagePage{}, fmt.Errorf("mode must be offline or refresh")
			}
			page, err := s.service.SearchMessagesContext(ctx, input.selector(), postmail.SearchOptions{Folder: input.Folder, Query: input.Query, Since: since, Before: before, Unread: input.Unread, Mode: input.Mode}, input.PageSize, input.Cursor)
			return nil, page, err
		})

	mcp.AddTool(s.mcp, &mcp.Tool{Name: "messages_triage", Title: "Triage messages", Description: "Compact inbox triage across selected connections: opaque id, from, subject, date, unread/flagged, attachment hint, and short preview. Start here before messages_get. Pass next_cursor back unchanged with identical filters. Use id (not uid) for Gmail and Microsoft messages.", Annotations: readOnly},
		func(ctx context.Context, _ *mcp.CallToolRequest, input messageSearchInput) (*mcp.CallToolResult, model.TriagePage, error) {
			page, err := s.service.TriageMessages(ctx, input.selector(), postmail.SearchOptions{Folder: input.Folder, Query: input.Query, Unread: input.Unread, Mode: input.Mode}, input.PageSize, input.Cursor)
			return nil, page, err
		})

	mcp.AddTool(s.mcp, &mcp.Tool{Name: "messages_unread_counts", Title: "Unread counts", Description: "Return unread counts per selected mail-capable connection for the inbox (or an explicit folder).", Annotations: readOnly},
		func(ctx context.Context, _ *mcp.CallToolRequest, input unreadCountsInput) (*mcp.CallToolResult, map[string]any, error) {
			summaries, err := s.service.UnreadCounts(ctx, input.selector(), input.Folder)
			return nil, map[string]any{"unread": summaries}, err
		})

	if s.profile != policy.MCPProfileReadonly {
		mcp.AddTool(s.mcp, &mcp.Tool{Name: "messages_send_prepare", Title: "Prepare message send", Description: "Prepare a plain-text or HTML email with up to 25 MiB total attachment data through exactly one mail connection. Prefer messages_draft_prepare when the operator should review before sending. Text-only is text/plain. HTML-only is multipart/alternative with a derived text/plain fallback. Both bodies are multipart/alternative as supplied. Returns a ten-minute opaque token and exact side-effect preview; no message is sent until operation_execute is called.", Annotations: readOnly},
			func(ctx context.Context, _ *mcp.CallToolRequest, input sendMessageInput) (*mcp.CallToolResult, model.PreparedOperation, error) {
				prepared, err := s.service.PrepareSend(ctx, model.SendMessage{ConnectionID: input.Connection, To: input.To, CC: input.CC, BCC: input.BCC, Subject: input.Subject, Text: input.Text, HTML: input.HTML, ReplyTo: input.ReplyTo, InReplyTo: input.InReplyTo, References: input.References, Attachments: mcpAttachments(input.Attachments)})
				return nil, prepared, err
			})

		mcp.AddTool(s.mcp, &mcp.Tool{Name: "messages_reply_prepare", Title: "Prepare message reply", Description: "Fetch one provider message by opaque id and prepare a threaded plain-text or HTML reply through the same exact connection, honoring Reply-To. Prefer a provider draft via messages_draft_prepare when the operator should review before sending. No message is sent until operation_execute.", Annotations: readOnly},
			func(ctx context.Context, _ *mcp.CallToolRequest, input messageReplyInput) (*mcp.CallToolResult, model.PreparedOperation, error) {
				prepared, err := s.service.PrepareReply(ctx, input.Connection, input.locator(), input.Text, input.HTML)
				return nil, prepared, err
			})

		mcp.AddTool(s.mcp, &mcp.Tool{Name: "messages_forward_prepare", Title: "Prepare message forward", Description: "Fetch one provider message by opaque id and prepare a forward through the same exact connection. Set verbatim=true to attach original parts without putting the original body into the preview. No message is sent until operation_execute.", Annotations: readOnly},
			func(ctx context.Context, _ *mcp.CallToolRequest, input messageForwardInput) (*mcp.CallToolResult, model.PreparedOperation, error) {
				var prepared model.PreparedOperation
				var err error
				if input.Verbatim {
					prepared, err = s.service.PrepareForwardVerbatim(ctx, input.Connection, input.locator(), input.To, input.Text)
				} else {
					prepared, err = s.service.PrepareForward(ctx, input.Connection, input.locator(), input.To, input.Text, input.HTML)
				}
				return nil, prepared, err
			})

		mcp.AddTool(s.mcp, &mcp.Tool{Name: "messages_draft_prepare", Title: "Prepare provider draft mutation", Description: "Preferred compose path for agent workflows: prepare create, update, or non-expunging delete of one provider-side draft through exactly one mail connection so the operator can review before sending. Identify existing drafts by opaque id. Attachment data is limited to 25 MiB total. No provider draft changes until operation_execute.", Annotations: readOnly},
			func(ctx context.Context, _ *mcp.CallToolRequest, input messageDraftInput) (*mcp.CallToolResult, model.PreparedOperation, error) {
				prepared, err := s.service.PrepareDraft(ctx, input.Connection, "mail.draft."+input.Action, input.locator(), input.Message.model())
				return nil, prepared, err
			})
	}

	mcp.AddTool(s.mcp, &mcp.Tool{Name: "messages_get", Title: "Get message", Description: "Fetch and decode one complete MIME message from an exact connection and opaque message id, including safe HTML, plain text, markdown approximation, threading headers, and attachment metadata.", Annotations: readOnly},
		func(ctx context.Context, _ *mcp.CallToolRequest, input messageGetInput) (*mcp.CallToolResult, model.MessageDetail, error) {
			if err := validateReadMode(input.Mode); err != nil {
				return nil, model.MessageDetail{}, err
			}
			detail, err := s.service.GetMessageModeContext(ctx, input.Connection, input.locator(), input.Mode)
			return nil, detail, err
		})

	mcp.AddTool(s.mcp, &mcp.Tool{Name: "messages_attachment_get", Title: "Get attachment chunk", Description: "Read a bounded base64 chunk from one immutable message-attachment snapshot. The maximum chunk is 1 MiB; pass both next_offset and next_cursor until omitted. Set extract_text=true for PDF attachments to return extracted plain text in text (and as UTF-8 data_base64). A final cursorless chunk can be returned without caching, while multi-chunk reads require cache.max_bytes capacity for the full encrypted snapshot.", Annotations: readOnly},
		func(ctx context.Context, _ *mcp.CallToolRequest, input attachmentGetInput) (*mcp.CallToolResult, attachmentChunkOutput, error) {
			if err := validateReadMode(input.Mode); err != nil {
				return nil, attachmentChunkOutput{}, err
			}
			if input.Offset < 0 {
				return nil, attachmentChunkOutput{}, fmt.Errorf("offset cannot be negative")
			}
			if input.Offset > 0 && input.Cursor == "" {
				return nil, attachmentChunkOutput{}, fmt.Errorf("cursor is required when offset is greater than zero")
			}
			if input.Limit == 0 {
				input.Limit = 256 << 10
			}
			if input.Limit < 1 || input.Limit > 1<<20 {
				return nil, attachmentChunkOutput{}, fmt.Errorf("limit must be between 1 and 1048576")
			}
			attachment, data, snapshotCursor, err := s.service.GetAttachmentSnapshotMode(ctx, input.Connection, input.locator(), input.AttachmentID, input.Mode, input.Cursor)
			if err != nil {
				return nil, attachmentChunkOutput{}, err
			}
			var extracted string
			if input.ExtractText {
				if !strings.Contains(strings.ToLower(attachment.ContentType), "pdf") {
					return nil, attachmentChunkOutput{}, fmt.Errorf("extract_text is only supported for PDF attachments")
				}
				extracted, err = postmail.ExtractPDFText(data)
				if err != nil {
					return nil, attachmentChunkOutput{}, err
				}
				return nil, attachmentChunkOutput{
					Attachment: attachment,
					Offset:     0,
					Text:       extracted,
					DataBase64: base64.StdEncoding.EncodeToString([]byte(extracted)),
				}, nil
			}
			if input.Offset > len(data) {
				return nil, attachmentChunkOutput{}, fmt.Errorf("offset exceeds attachment size")
			}
			end := min(input.Offset+input.Limit, len(data))
			if end < len(data) && snapshotCursor == "" {
				return nil, attachmentChunkOutput{}, fmt.Errorf("attachment exceeds encrypted cache capacity; increase cache.max_bytes or request an attachment no larger than the 1 MiB chunk limit")
			}
			output := attachmentChunkOutput{Attachment: attachment, Offset: input.Offset, DataBase64: base64.StdEncoding.EncodeToString(data[input.Offset:end])}
			if end < len(data) {
				output.NextOffset = end
				output.NextCursor = snapshotCursor
			}
			return nil, output, nil
		})

	if s.profile != policy.MCPProfileReadonly {
		mcp.AddTool(s.mcp, &mcp.Tool{Name: "messages_action_prepare", Title: "Prepare message action", Description: "Prepare a mark, move, archive, trash, or junk action for one or more provider messages (opaque id, uid, or uids). No provider state changes until operation_execute.", Annotations: readOnly},
			func(ctx context.Context, _ *mcp.CallToolRequest, input messageActionInput) (*mcp.CallToolResult, model.PreparedOperation, error) {
				prepared, err := s.service.PrepareMailAction(ctx, input.Connection, "mail."+input.Action, service.MailAction{ID: input.ID, Folder: input.Folder, UID: input.UID, UIDs: input.UIDs, Destination: input.Destination, Seen: input.Seen, Flagged: input.Flagged})
				return nil, prepared, err
			})
	}

	mcp.AddTool(s.mcp, &mcp.Tool{Name: "events_list", Title: "List calendar events", Description: "List and search calendar events across selected connections in an optional time range, up to 500 per page. ICS feeds, CalDAV collections, and operator-authorized OAuth calendars use the same event shape. Live-first is the default with stale-cache fallback; mode=offline is cache-only and treats a miss as an error rather than an empty calendar; mode=refresh refuses stale fallback. Pass next_cursor back unchanged with identical filters.", Annotations: readOnly},
		func(ctx context.Context, _ *mcp.CallToolRequest, input eventListInput) (*mcp.CallToolResult, model.EventPage, error) {
			start, err := optionalTime(input.Start)
			if err != nil {
				return nil, model.EventPage{}, fmt.Errorf("start: %w", err)
			}
			end, err := optionalTime(input.End)
			if err != nil {
				return nil, model.EventPage{}, fmt.Errorf("end: %w", err)
			}
			if input.Mode != "" && input.Mode != "offline" && input.Mode != "refresh" {
				return nil, model.EventPage{}, fmt.Errorf("mode must be offline or refresh")
			}
			page, err := s.service.ListEventsMode(ctx, input.selector(), start, end, input.Query, input.PageSize, input.Cursor, input.Mode)
			return nil, page, err
		})

	mcp.AddTool(s.mcp, &mcp.Tool{Name: "event_ics_generate", Title: "Generate ICS file", Description: "Generate a portable .ics calendar file without modifying any provider. Omit method for a VEVENT file; set method to request or cancel for METHOD invitations. Cancel requires id. Returns structured event data, the raw ICS string, and an embedded text/calendar resource.", Annotations: readOnly},
		func(_ context.Context, _ *mcp.CallToolRequest, input eventICSInput) (*mcp.CallToolResult, icsOutput, error) {
			start, err := time.Parse(time.RFC3339, input.Start)
			if err != nil {
				return nil, icsOutput{}, fmt.Errorf("start: %w", err)
			}
			end, err := time.Parse(time.RFC3339, input.End)
			if err != nil {
				return nil, icsOutput{}, fmt.Errorf("end: %w", err)
			}
			event := model.Event{ID: input.ID, Title: input.Title, Description: input.Description, Location: input.Location, Start: start, End: end, AllDay: input.AllDay, Attendees: input.Attendees, Organizer: input.Organizer, Sequence: input.Sequence}
			method := strings.ToLower(strings.TrimSpace(input.Method))
			var data string
			switch method {
			case "":
				event, data, err = s.service.GenerateICS(event)
			case "request":
				event, data, err = calendar.GenerateInvitation(event, false)
			case "cancel":
				if strings.TrimSpace(input.ID) == "" {
					return nil, icsOutput{}, fmt.Errorf("cancel invitations require id")
				}
				event, data, err = calendar.GenerateInvitation(event, true)
			default:
				return nil, icsOutput{}, fmt.Errorf("method must be request or cancel")
			}
			if err != nil {
				return nil, icsOutput{}, err
			}
			filename := calendar.Filename(event)
			result := &mcp.CallToolResult{Content: []mcp.Content{&mcp.EmbeddedResource{Resource: &mcp.ResourceContents{URI: "posthouse://generated/" + filename, MIMEType: "text/calendar", Text: data}}}}
			return result, icsOutput{Event: event, Filename: filename, MIMEType: "text/calendar", ICS: data}, nil
		})

	if s.profile != policy.MCPProfileReadonly {
		mcp.AddTool(s.mcp, &mcp.Tool{Name: "event_create_prepare", Title: "Prepare event create", Description: "Prepare creation of one event in an exact CalDAV connection and collection. No event is written until operation_execute.", Annotations: readOnly},
			func(ctx context.Context, _ *mcp.CallToolRequest, input eventMutationInput) (*mcp.CallToolResult, model.PreparedOperation, error) {
				prepared, err := s.service.PrepareCalendarWrite(ctx, input.Connection, "calendar.create", input.Event)
				return nil, prepared, err
			})
		mcp.AddTool(s.mcp, &mcp.Tool{Name: "event_update_prepare", Title: "Prepare event update", Description: "Prepare an ETag-guarded update to one CalDAV event. No event is written until operation_execute.", Annotations: readOnly},
			func(ctx context.Context, _ *mcp.CallToolRequest, input eventMutationInput) (*mcp.CallToolResult, model.PreparedOperation, error) {
				prepared, err := s.service.PrepareCalendarWrite(ctx, input.Connection, "calendar.update", input.Event)
				return nil, prepared, err
			})
		mcp.AddTool(s.mcp, &mcp.Tool{Name: "event_delete_prepare", Title: "Prepare event delete", Description: "Prepare an ETag-guarded delete of one CalDAV event. No event is deleted until operation_execute.", Annotations: readOnly},
			func(ctx context.Context, _ *mcp.CallToolRequest, input eventDeleteInput) (*mcp.CallToolResult, model.PreparedOperation, error) {
				prepared, err := s.service.PrepareCalendarDelete(ctx, input.Connection, input.Collection, input.Href, input.ETag, input.RecurrenceID)
				return nil, prepared, err
			})
	}

	mcp.AddTool(s.mcp, &mcp.Tool{Name: "operation_show", Title: "Show prepared operation", Description: "Show the exact preview and status for an opaque prepared-operation token without executing it.", Annotations: readOnly},
		func(ctx context.Context, _ *mcp.CallToolRequest, input operationInput) (*mcp.CallToolResult, model.PreparedOperation, error) {
			operation, err := s.service.OperationShow(ctx, input.Token)
			return nil, operation, err
		})

	if s.profile != policy.MCPProfileReadonly {
		mcp.AddTool(s.mcp, &mcp.Tool{Name: "operation_execute", Title: "Execute prepared operation", Description: "Execute one confirmed prepared-operation token exactly once. Repeated calls return the original result; uncertain SMTP outcomes are never retried. Honors policy.deny and POSTHOUSE_POLICY_DENY.", Annotations: executeWrite},
			func(ctx context.Context, _ *mcp.CallToolRequest, input operationInput) (*mcp.CallToolResult, model.OperationResult, error) {
				result, err := s.service.ExecuteOperation(ctx, input.Token)
				return nil, result, err
			})
	}

	mcp.AddTool(s.mcp, &mcp.Tool{Name: "connection_doctor", Title: "Doctor connection", Description: "Run non-mutating secret, TLS, authentication, IMAP/SMTP, and CalDAV discovery checks for one exact connection.", Annotations: readOnly},
		func(ctx context.Context, _ *mcp.CallToolRequest, input connectionInput) (*mcp.CallToolResult, model.DoctorResult, error) {
			result, err := s.service.DoctorConnection(ctx, input.Connection)
			return nil, result, err
		})
	mcp.AddTool(s.mcp, &mcp.Tool{Name: "sync", Title: "Sync selected sources", Description: "Refresh the encrypted local cache from selected mail and calendar sources. Returns per-protocol counts and structured partial source errors.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &nondestructive, OpenWorldHint: &openWorld}},
		func(ctx context.Context, _ *mcp.CallToolRequest, input selectorInput) (*mcp.CallToolResult, map[string]any, error) {
			result, err := s.service.Sync(ctx, input.selector())
			return nil, result, err
		})
	mcp.AddTool(s.mcp, &mcp.Tool{Name: "cache_status", Title: "Cache status", Description: "Return encrypted local-cache usage and limits without returning cached provider content.", Annotations: readOnly},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, state.Stats, error) {
			status, err := s.service.CacheStatus(ctx)
			return nil, status, err
		})
}

func optionalTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, value)
}

func validateReadMode(mode string) error {
	if mode != "" && mode != "offline" && mode != "refresh" {
		return fmt.Errorf("mode must be offline or refresh")
	}
	return nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
