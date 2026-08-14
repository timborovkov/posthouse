package mcpserver

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/posthousehq/posthouse/internal/calendar"
	postmail "github.com/posthousehq/posthouse/internal/mail"
	"github.com/posthousehq/posthouse/internal/model"
	"github.com/posthousehq/posthouse/internal/service"
)

const Version = "0.1.0"

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
	return model.Selector{Connections: input.Connections, Category: input.Category, Labels: input.Labels}
}

type messageSearchInput struct {
	selectorInput
	pageInput
	Folder string `json:"folder,omitempty"`
	Query  string `json:"query,omitempty" jsonschema:"text to search in message headers and bodies"`
	Since  string `json:"since,omitempty" jsonschema:"inclusive RFC3339 timestamp"`
	Before string `json:"before,omitempty" jsonschema:"exclusive RFC3339 timestamp"`
	Unread bool   `json:"unread,omitempty"`
}

type sendMessageInput struct {
	Connection string   `json:"connection" jsonschema:"exact connection ID or unique name"`
	To         []string `json:"to"`
	CC         []string `json:"cc,omitempty"`
	BCC        []string `json:"bcc,omitempty"`
	Subject    string   `json:"subject"`
	Text       string   `json:"text"`
	ReplyTo    string   `json:"reply_to,omitempty"`
}

type operationOutput struct {
	OK bool `json:"ok"`
}

type eventListInput struct {
	selectorInput
	pageInput
	Start string `json:"start,omitempty" jsonschema:"inclusive RFC3339 timestamp"`
	End   string `json:"end,omitempty" jsonschema:"exclusive RFC3339 timestamp"`
	Query string `json:"query,omitempty" jsonschema:"text to find in title description or location"`
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
	write := &mcp.ToolAnnotations{DestructiveHint: &nondestructive, OpenWorldHint: &openWorld}

	mcp.AddTool(s.mcp, &mcp.Tool{Name: "connections_list", Title: "List connections", Description: "List configured mail and calendar connections by name, category, and labels, up to 200 per page. Pass next_cursor back unchanged with identical filters. Secret values and secret environment-variable names are never returned.", Annotations: readOnly},
		func(_ context.Context, _ *mcp.CallToolRequest, input connectionsListInput) (*mcp.CallToolResult, model.ConnectionPage, error) {
			page, err := s.service.ListConnections(input.selector(), input.PageSize, input.Cursor)
			return nil, page, err
		})

	mcp.AddTool(s.mcp, &mcp.Tool{Name: "messages_search", Title: "Search messages", Description: "List or search messages across selected IMAP connections, up to 100 per page. Pass next_cursor back unchanged with identical filters; cursors validate each mailbox UID namespace.", Annotations: readOnly},
		func(_ context.Context, _ *mcp.CallToolRequest, input messageSearchInput) (*mcp.CallToolResult, model.MessagePage, error) {
			since, err := optionalTime(input.Since)
			if err != nil {
				return nil, model.MessagePage{}, fmt.Errorf("since: %w", err)
			}
			before, err := optionalTime(input.Before)
			if err != nil {
				return nil, model.MessagePage{}, fmt.Errorf("before: %w", err)
			}
			page, err := s.service.SearchMessages(input.selector(), postmail.SearchOptions{Folder: input.Folder, Query: input.Query, Since: since, Before: before, Unread: input.Unread}, input.PageSize, input.Cursor)
			return nil, page, err
		})

	mcp.AddTool(s.mcp, &mcp.Tool{Name: "messages_send", Title: "Send message", Description: "Send a plain-text email through exactly one selected SMTP connection. This has an external side effect and should be confirmed with the user.", Annotations: write},
		func(_ context.Context, _ *mcp.CallToolRequest, input sendMessageInput) (*mcp.CallToolResult, operationOutput, error) {
			err := s.service.SendMessage(model.SendMessage{ConnectionID: input.Connection, To: input.To, CC: input.CC, BCC: input.BCC, Subject: input.Subject, Text: input.Text, ReplyTo: input.ReplyTo})
			return nil, operationOutput{OK: err == nil}, err
		})

	mcp.AddTool(s.mcp, &mcp.Tool{Name: "events_list", Title: "List calendar events", Description: "List and search read-only ICS calendar feeds across selected connections in an optional time range, up to 500 per page. Pass next_cursor back unchanged with identical filters.", Annotations: readOnly},
		func(ctx context.Context, _ *mcp.CallToolRequest, input eventListInput) (*mcp.CallToolResult, model.EventPage, error) {
			start, err := optionalTime(input.Start)
			if err != nil {
				return nil, model.EventPage{}, fmt.Errorf("start: %w", err)
			}
			end, err := optionalTime(input.End)
			if err != nil {
				return nil, model.EventPage{}, fmt.Errorf("end: %w", err)
			}
			page, err := s.service.ListEvents(ctx, input.selector(), start, end, input.Query, input.PageSize, input.Cursor)
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
}

func optionalTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, value)
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
