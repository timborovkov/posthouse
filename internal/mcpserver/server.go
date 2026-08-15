package mcpserver

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/timborovkov/posthouse/internal/calendar"
	postmail "github.com/timborovkov/posthouse/internal/mail"
	"github.com/timborovkov/posthouse/internal/model"
	"github.com/timborovkov/posthouse/internal/service"
	"github.com/timborovkov/posthouse/internal/state"
)

const Version = "0.2.0"

type Server struct {
	service *service.Service
	mcp     *mcp.Server
}

func New(service *service.Service) *Server {
	server := &Server{service: service}
	server.mcp = mcp.NewServer(&mcp.Implementation{Name: "posthouse", Version: Version}, nil)
	server.registerTools()
	return server
}

func (s *Server) RunStdio(ctx context.Context) error {
	return s.mcp.Run(ctx, &mcp.StdioTransport{})
}

func (s *Server) RunHTTP(ctx context.Context, address string, token string, logger *slog.Logger) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid HTTP address %q: %w", address, err)
	}
	if !isLoopback(host) && token == "" {
		return fmt.Errorf("POSTHOUSE_MCP_TOKEN is required when listening outside localhost")
	}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.mcp }, &mcp.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true, Logger: logger, SessionTimeout: 5 * time.Minute,
	})
	mux := http.NewServeMux()
	mux.Handle("/mcp", authenticate(http.MaxBytesHandler(handler, 4<<20), token))
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
	httpServer := &http.Server{Addr: address, Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 2 * time.Minute}
	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownContext)
	}()
	logger.Info("MCP server listening", "address", address, "endpoint", "/mcp")
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
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

type sendMessageInput struct {
	Connection  string               `json:"connection" jsonschema:"exact connection ID or unique name"`
	To          []string             `json:"to"`
	CC          []string             `json:"cc,omitempty"`
	BCC         []string             `json:"bcc,omitempty"`
	Subject     string               `json:"subject"`
	Text        string               `json:"text"`
	ReplyTo     string               `json:"reply_to,omitempty"`
	InReplyTo   string               `json:"in_reply_to,omitempty"`
	References  []string             `json:"references,omitempty"`
	Attachments []mcpAttachmentInput `json:"attachments,omitempty"`
}

type mcpAttachmentInput struct {
	Name        string `json:"name"`
	ContentType string `json:"content_type,omitempty"`
	Data        []byte `json:"data" jsonschema:"base64-encoded attachment bytes"`
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
	Folder     string `json:"folder,omitempty"`
	UID        uint32 `json:"uid"`
	Text       string `json:"text,omitempty"`
}

type messageForwardInput struct {
	messageReplyInput
	To []string `json:"to"`
}

type messageDraftInput struct {
	Connection string          `json:"connection"`
	Action     string          `json:"action" jsonschema:"create, update, or delete"`
	Folder     string          `json:"folder,omitempty"`
	UID        uint32          `json:"uid,omitempty"`
	Message    mcpDraftMessage `json:"message,omitempty"`
}

type mcpDraftMessage struct {
	To          []string             `json:"to,omitempty"`
	CC          []string             `json:"cc,omitempty"`
	BCC         []string             `json:"bcc,omitempty"`
	Subject     string               `json:"subject,omitempty"`
	Text        string               `json:"text,omitempty"`
	ReplyTo     string               `json:"reply_to,omitempty"`
	InReplyTo   string               `json:"in_reply_to,omitempty"`
	References  []string             `json:"references,omitempty"`
	Attachments []mcpAttachmentInput `json:"attachments,omitempty"`
}

func (input mcpDraftMessage) model() model.SendMessage {
	return model.SendMessage{To: input.To, CC: input.CC, BCC: input.BCC, Subject: input.Subject, Text: input.Text, ReplyTo: input.ReplyTo, InReplyTo: input.InReplyTo, References: input.References, Attachments: mcpAttachments(input.Attachments)}
}

type operationOutput struct {
	OK bool `json:"ok"`
}

type operationInput struct {
	Token string `json:"token"`
}

type messageGetInput struct {
	Connection string `json:"connection"`
	Folder     string `json:"folder,omitempty"`
	UID        uint32 `json:"uid"`
	Mode       string `json:"mode,omitempty" jsonschema:"empty for live-first stale fallback, offline for cache-only, or refresh for live-only"`
}

type attachmentGetInput struct {
	messageGetInput
	AttachmentID string `json:"attachment_id"`
	Offset       int    `json:"offset,omitempty"`
	Limit        int    `json:"limit,omitempty"`
}

type attachmentChunkOutput struct {
	Attachment model.Attachment `json:"attachment"`
	Offset     int              `json:"offset"`
	NextOffset int              `json:"next_offset,omitempty"`
	DataBase64 string           `json:"data_base64"`
}

type messageActionInput struct {
	Connection  string `json:"connection"`
	Action      string `json:"action" jsonschema:"mark, move, archive, or trash"`
	Folder      string `json:"folder,omitempty"`
	UID         uint32 `json:"uid"`
	Destination string `json:"destination,omitempty"`
	Seen        *bool  `json:"seen,omitempty"`
	Flagged     *bool  `json:"flagged,omitempty"`
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

	mcp.AddTool(s.mcp, &mcp.Tool{Name: "messages_search", Title: "Search messages", Description: "List or search messages across selected IMAP connections, up to 100 per page. Pass next_cursor back unchanged with identical filters; cursors validate each mailbox UID namespace.", Annotations: readOnly},
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

	mcp.AddTool(s.mcp, &mcp.Tool{Name: "messages_send_prepare", Title: "Prepare message send", Description: "Prepare a plain-text email with optional attachments through exactly one SMTP connection. Returns a ten-minute opaque token and exact side-effect preview; no message is sent until operation_execute is called.", Annotations: readOnly},
		func(ctx context.Context, _ *mcp.CallToolRequest, input sendMessageInput) (*mcp.CallToolResult, model.PreparedOperation, error) {
			prepared, err := s.service.PrepareSend(ctx, model.SendMessage{ConnectionID: input.Connection, To: input.To, CC: input.CC, BCC: input.BCC, Subject: input.Subject, Text: input.Text, ReplyTo: input.ReplyTo, InReplyTo: input.InReplyTo, References: input.References, Attachments: mcpAttachments(input.Attachments)})
			return nil, prepared, err
		})

	mcp.AddTool(s.mcp, &mcp.Tool{Name: "messages_reply_prepare", Title: "Prepare message reply", Description: "Fetch one provider message and prepare a threaded plain-text reply through the same exact connection, honoring Reply-To. No message is sent until operation_execute.", Annotations: readOnly},
		func(ctx context.Context, _ *mcp.CallToolRequest, input messageReplyInput) (*mcp.CallToolResult, model.PreparedOperation, error) {
			prepared, err := s.service.PrepareReply(ctx, input.Connection, input.Folder, input.UID, input.Text)
			return nil, prepared, err
		})

	mcp.AddTool(s.mcp, &mcp.Tool{Name: "messages_forward_prepare", Title: "Prepare message forward", Description: "Fetch one provider message and prepare a plain-text forward through the same exact connection. No message is sent until operation_execute.", Annotations: readOnly},
		func(ctx context.Context, _ *mcp.CallToolRequest, input messageForwardInput) (*mcp.CallToolResult, model.PreparedOperation, error) {
			prepared, err := s.service.PrepareForward(ctx, input.Connection, input.Folder, input.UID, input.To, input.Text)
			return nil, prepared, err
		})

	mcp.AddTool(s.mcp, &mcp.Tool{Name: "messages_draft_prepare", Title: "Prepare provider draft mutation", Description: "Prepare create, update, or non-expunging delete of one provider-side draft through exactly one IMAP connection.", Annotations: readOnly},
		func(ctx context.Context, _ *mcp.CallToolRequest, input messageDraftInput) (*mcp.CallToolResult, model.PreparedOperation, error) {
			prepared, err := s.service.PrepareDraft(ctx, input.Connection, "mail.draft."+input.Action, input.Folder, input.UID, input.Message.model())
			return nil, prepared, err
		})

	mcp.AddTool(s.mcp, &mcp.Tool{Name: "messages_get", Title: "Get message", Description: "Fetch and decode one complete MIME message from an exact connection and UID, including safe HTML, text, threading headers, and attachment metadata.", Annotations: readOnly},
		func(_ context.Context, _ *mcp.CallToolRequest, input messageGetInput) (*mcp.CallToolResult, model.MessageDetail, error) {
			if err := validateReadMode(input.Mode); err != nil {
				return nil, model.MessageDetail{}, err
			}
			detail, err := s.service.GetMessageMode(input.Connection, input.Folder, input.UID, input.Mode)
			return nil, detail, err
		})

	mcp.AddTool(s.mcp, &mcp.Tool{Name: "messages_attachment_get", Title: "Get attachment chunk", Description: "Read a bounded base64 chunk from one message attachment. The maximum chunk is 1 MiB; pass next_offset until omitted.", Annotations: readOnly},
		func(ctx context.Context, _ *mcp.CallToolRequest, input attachmentGetInput) (*mcp.CallToolResult, attachmentChunkOutput, error) {
			if err := validateReadMode(input.Mode); err != nil {
				return nil, attachmentChunkOutput{}, err
			}
			if input.Offset < 0 {
				return nil, attachmentChunkOutput{}, fmt.Errorf("offset cannot be negative")
			}
			if input.Limit == 0 {
				input.Limit = 256 << 10
			}
			if input.Limit < 1 || input.Limit > 1<<20 {
				return nil, attachmentChunkOutput{}, fmt.Errorf("limit must be between 1 and 1048576")
			}
			attachment, data, err := s.service.GetAttachmentMode(ctx, input.Connection, input.Folder, input.UID, input.AttachmentID, input.Mode)
			if err != nil {
				return nil, attachmentChunkOutput{}, err
			}
			if input.Offset > len(data) {
				return nil, attachmentChunkOutput{}, fmt.Errorf("offset exceeds attachment size")
			}
			end := min(input.Offset+input.Limit, len(data))
			output := attachmentChunkOutput{Attachment: attachment, Offset: input.Offset, DataBase64: base64.StdEncoding.EncodeToString(data[input.Offset:end])}
			if end < len(data) {
				output.NextOffset = end
			}
			return nil, output, nil
		})

	mcp.AddTool(s.mcp, &mcp.Tool{Name: "messages_action_prepare", Title: "Prepare message action", Description: "Prepare a mark, move, archive, or trash action for exactly one provider message. No provider state changes until operation_execute.", Annotations: readOnly},
		func(ctx context.Context, _ *mcp.CallToolRequest, input messageActionInput) (*mcp.CallToolResult, model.PreparedOperation, error) {
			prepared, err := s.service.PrepareMailAction(ctx, input.Connection, "mail."+input.Action, service.MailAction{Folder: input.Folder, UID: input.UID, Destination: input.Destination, Seen: input.Seen, Flagged: input.Flagged})
			return nil, prepared, err
		})

	mcp.AddTool(s.mcp, &mcp.Tool{Name: "events_list", Title: "List calendar events", Description: "List and search ICS feeds and CalDAV calendar collections across selected connections in an optional time range, up to 500 per page. Pass next_cursor back unchanged with identical filters.", Annotations: readOnly},
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

	mcp.AddTool(s.mcp, &mcp.Tool{Name: "event_ics_generate", Title: "Generate ICS file", Description: "Generate a portable .ics calendar file without modifying any provider. Returns structured event data, the raw ICS string, and an embedded text/calendar resource.", Annotations: readOnly},
		func(_ context.Context, _ *mcp.CallToolRequest, input eventICSInput) (*mcp.CallToolResult, icsOutput, error) {
			start, err := time.Parse(time.RFC3339, input.Start)
			if err != nil {
				return nil, icsOutput{}, fmt.Errorf("start: %w", err)
			}
			end, err := time.Parse(time.RFC3339, input.End)
			if err != nil {
				return nil, icsOutput{}, fmt.Errorf("end: %w", err)
			}
			event, data, err := s.service.GenerateICS(model.Event{ID: input.ID, Title: input.Title, Description: input.Description, Location: input.Location, Start: start, End: end, AllDay: input.AllDay, Attendees: input.Attendees, Organizer: input.Organizer})
			if err != nil {
				return nil, icsOutput{}, err
			}
			filename := calendar.Filename(event)
			result := &mcp.CallToolResult{Content: []mcp.Content{&mcp.EmbeddedResource{Resource: &mcp.ResourceContents{URI: "posthouse://generated/" + filename, MIMEType: "text/calendar", Text: data}}}}
			return result, icsOutput{Event: event, Filename: filename, MIMEType: "text/calendar", ICS: data}, nil
		})

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

	mcp.AddTool(s.mcp, &mcp.Tool{Name: "operation_show", Title: "Show prepared operation", Description: "Show the exact preview and status for an opaque prepared-operation token without executing it.", Annotations: readOnly},
		func(ctx context.Context, _ *mcp.CallToolRequest, input operationInput) (*mcp.CallToolResult, model.PreparedOperation, error) {
			operation, err := s.service.OperationShow(ctx, input.Token)
			return nil, operation, err
		})
	mcp.AddTool(s.mcp, &mcp.Tool{Name: "operation_execute", Title: "Execute prepared operation", Description: "Execute one confirmed prepared-operation token exactly once. Repeated calls return the original result; uncertain SMTP outcomes are never retried.", Annotations: executeWrite},
		func(ctx context.Context, _ *mcp.CallToolRequest, input operationInput) (*mcp.CallToolResult, model.OperationResult, error) {
			result, err := s.service.ExecuteOperation(ctx, input.Token)
			return nil, result, err
		})

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

func authenticate(next http.Handler, token string) http.Handler {
	if token == "" {
		return next
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		provided := request.Header.Get("Authorization")
		wanted := "Bearer " + token
		if subtle.ConstantTimeCompare([]byte(provided), []byte(wanted)) != 1 {
			writer.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
