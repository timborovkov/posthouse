package mcpserver

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/timborovkov/posthouse/internal/calendar"
	postmail "github.com/timborovkov/posthouse/internal/mail"
	"github.com/timborovkov/posthouse/internal/model"
	"github.com/timborovkov/posthouse/internal/service"
)

func (s *Server) registerREST(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1", s.restCatalog)
	mux.HandleFunc("GET /v1/connections", s.restConnections)
	mux.HandleFunc("POST /v1/connections/doctor", s.restConnectionDoctor)
	mux.HandleFunc("POST /v1/mail/search", s.restMailSearch)
	mux.HandleFunc("POST /v1/mail/get", s.restMailGet)
	mux.HandleFunc("POST /v1/mail/attachment", s.restMailAttachment)
	mux.HandleFunc("POST /v1/mail/send", s.restMailSend)
	mux.HandleFunc("POST /v1/mail/reply", s.restMailReply)
	mux.HandleFunc("POST /v1/mail/forward", s.restMailForward)
	mux.HandleFunc("POST /v1/mail/draft", s.restMailDraft)
	mux.HandleFunc("POST /v1/mail/action", s.restMailAction)
	mux.HandleFunc("POST /v1/calendar/events", s.restCalendarEvents)
	mux.HandleFunc("POST /v1/calendar/ics", s.restCalendarICS)
	mux.HandleFunc("POST /v1/calendar/create", s.restCalendarCreate)
	mux.HandleFunc("POST /v1/calendar/update", s.restCalendarUpdate)
	mux.HandleFunc("POST /v1/calendar/delete", s.restCalendarDelete)
	mux.HandleFunc("POST /v1/operations/show", s.restOperationShow)
	mux.HandleFunc("POST /v1/operations/execute", s.restOperationExecute)
	mux.HandleFunc("POST /v1/sync", s.restSync)
	mux.HandleFunc("GET /v1/cache", s.restCacheStatus)
}

func (s *Server) restCatalog(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]any{
		"name":    "posthouse",
		"version": Version,
		"note":    "Personal deployment. Reads may fan out across a selector. Writes resolve to exactly one connection and return a prepared token; only operations/execute performs the provider side effect.",
		"auth":    []string{"Authorization: Bearer <POSTHOUSE_ACCESS_KEY>", "X-Posthouse-Key: <POSTHOUSE_ACCESS_KEY>"},
		"mcp":     "/mcp",
		"endpoints": []string{
			"GET /v1",
			"GET /v1/connections",
			"POST /v1/connections/doctor",
			"POST /v1/mail/search",
			"POST /v1/mail/get",
			"POST /v1/mail/attachment",
			"POST /v1/mail/send",
			"POST /v1/mail/reply",
			"POST /v1/mail/forward",
			"POST /v1/mail/draft",
			"POST /v1/mail/action",
			"POST /v1/calendar/events",
			"POST /v1/calendar/ics",
			"POST /v1/calendar/create",
			"POST /v1/calendar/update",
			"POST /v1/calendar/delete",
			"POST /v1/operations/show",
			"POST /v1/operations/execute",
			"POST /v1/sync",
			"GET /v1/cache",
		},
	})
}

func (s *Server) restConnections(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	input := connectionsListInput{
		selectorInput: selectorInput{
			Connections: splitQuery(query["connection"]),
			Category:    query.Get("category"),
			Labels:      splitQuery(query["label"]),
			Collections: splitQuery(query["collection"]),
			Capability:  query.Get("capability"),
		},
		pageInput: pageInput{Cursor: query.Get("cursor")},
	}
	if raw := query.Get("page_size"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeError(writer, http.StatusBadRequest, fmt.Errorf("page_size must be a positive integer"))
			return
		}
		input.PageSize = n
	}
	page, err := s.service.ListConnections(input.selector(), input.PageSize, input.Cursor)
	writeResult(writer, page, err)
}

func (s *Server) restConnectionDoctor(writer http.ResponseWriter, request *http.Request) {
	var input connectionInput
	if !decodeJSON(writer, request, &input) {
		return
	}
	result, err := s.service.DoctorConnection(request.Context(), input.Connection)
	writeResult(writer, result, err)
}

func (s *Server) restMailSearch(writer http.ResponseWriter, request *http.Request) {
	var input messageSearchInput
	if !decodeJSON(writer, request, &input) {
		return
	}
	since, err := optionalTime(input.Since)
	if err != nil {
		writeError(writer, http.StatusBadRequest, fmt.Errorf("since: %w", err))
		return
	}
	before, err := optionalTime(input.Before)
	if err != nil {
		writeError(writer, http.StatusBadRequest, fmt.Errorf("before: %w", err))
		return
	}
	if input.Mode != "" && input.Mode != "offline" && input.Mode != "refresh" {
		writeError(writer, http.StatusBadRequest, fmt.Errorf("mode must be offline or refresh"))
		return
	}
	page, err := s.service.SearchMessagesContext(request.Context(), input.selector(), postmail.SearchOptions{Folder: input.Folder, Query: input.Query, Since: since, Before: before, Unread: input.Unread, Mode: input.Mode}, input.PageSize, input.Cursor)
	writeResult(writer, page, err)
}

func (s *Server) restMailGet(writer http.ResponseWriter, request *http.Request) {
	var input messageGetInput
	if !decodeJSON(writer, request, &input) {
		return
	}
	if err := validateReadMode(input.Mode); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	detail, err := s.service.GetMessageModeContext(request.Context(), input.Connection, input.Folder, input.UID, input.Mode)
	writeResult(writer, detail, err)
}

func (s *Server) restMailAttachment(writer http.ResponseWriter, request *http.Request) {
	var input attachmentGetInput
	if !decodeJSON(writer, request, &input) {
		return
	}
	if err := validateReadMode(input.Mode); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if input.Offset < 0 {
		writeError(writer, http.StatusBadRequest, fmt.Errorf("offset cannot be negative"))
		return
	}
	if input.Offset > 0 && input.Cursor == "" {
		writeError(writer, http.StatusBadRequest, fmt.Errorf("cursor is required when offset is greater than zero"))
		return
	}
	if input.Limit == 0 {
		input.Limit = 256 << 10
	}
	if input.Limit < 1 || input.Limit > 1<<20 {
		writeError(writer, http.StatusBadRequest, fmt.Errorf("limit must be between 1 and 1048576"))
		return
	}
	attachment, data, snapshotCursor, err := s.service.GetAttachmentSnapshotMode(request.Context(), input.Connection, input.Folder, input.UID, input.AttachmentID, input.Mode, input.Cursor)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if input.Offset > len(data) {
		writeError(writer, http.StatusBadRequest, fmt.Errorf("offset exceeds attachment size"))
		return
	}
	end := min(input.Offset+input.Limit, len(data))
	if end < len(data) && snapshotCursor == "" {
		writeError(writer, http.StatusBadRequest, fmt.Errorf("attachment exceeds encrypted cache capacity; increase cache.max_bytes or request an attachment no larger than the 1 MiB chunk limit"))
		return
	}
	output := attachmentChunkOutput{Attachment: attachment, Offset: input.Offset, DataBase64: encodeBase64(data[input.Offset:end])}
	if end < len(data) {
		output.NextOffset = end
		output.NextCursor = snapshotCursor
	}
	writeJSON(writer, http.StatusOK, output)
}

func (s *Server) restMailSend(writer http.ResponseWriter, request *http.Request) {
	var input sendMessageInput
	if !decodeJSON(writer, request, &input) {
		return
	}
	prepared, err := s.service.PrepareSend(request.Context(), model.SendMessage{ConnectionID: input.Connection, To: input.To, CC: input.CC, BCC: input.BCC, Subject: input.Subject, Text: input.Text, HTML: input.HTML, ReplyTo: input.ReplyTo, InReplyTo: input.InReplyTo, References: input.References, Attachments: mcpAttachments(input.Attachments)})
	writeResult(writer, prepared, err)
}

func (s *Server) restMailReply(writer http.ResponseWriter, request *http.Request) {
	var input messageReplyInput
	if !decodeJSON(writer, request, &input) {
		return
	}
	prepared, err := s.service.PrepareReply(request.Context(), input.Connection, input.Folder, input.UID, input.Text, input.HTML)
	writeResult(writer, prepared, err)
}

func (s *Server) restMailForward(writer http.ResponseWriter, request *http.Request) {
	var input messageForwardInput
	if !decodeJSON(writer, request, &input) {
		return
	}
	prepared, err := s.service.PrepareForward(request.Context(), input.Connection, input.Folder, input.UID, input.To, input.Text, input.HTML)
	writeResult(writer, prepared, err)
}

func (s *Server) restMailDraft(writer http.ResponseWriter, request *http.Request) {
	var input messageDraftInput
	if !decodeJSON(writer, request, &input) {
		return
	}
	prepared, err := s.service.PrepareDraft(request.Context(), input.Connection, "mail.draft."+input.Action, input.Folder, input.UID, input.Message.model())
	writeResult(writer, prepared, err)
}

func (s *Server) restMailAction(writer http.ResponseWriter, request *http.Request) {
	var input messageActionInput
	if !decodeJSON(writer, request, &input) {
		return
	}
	prepared, err := s.service.PrepareMailAction(request.Context(), input.Connection, "mail."+input.Action, service.MailAction{Folder: input.Folder, UID: input.UID, Destination: input.Destination, Seen: input.Seen, Flagged: input.Flagged})
	writeResult(writer, prepared, err)
}

func (s *Server) restCalendarEvents(writer http.ResponseWriter, request *http.Request) {
	var input eventListInput
	if !decodeJSON(writer, request, &input) {
		return
	}
	start, err := optionalTime(input.Start)
	if err != nil {
		writeError(writer, http.StatusBadRequest, fmt.Errorf("start: %w", err))
		return
	}
	end, err := optionalTime(input.End)
	if err != nil {
		writeError(writer, http.StatusBadRequest, fmt.Errorf("end: %w", err))
		return
	}
	if input.Mode != "" && input.Mode != "offline" && input.Mode != "refresh" {
		writeError(writer, http.StatusBadRequest, fmt.Errorf("mode must be offline or refresh"))
		return
	}
	page, err := s.service.ListEventsMode(request.Context(), input.selector(), start, end, input.Query, input.PageSize, input.Cursor, input.Mode)
	writeResult(writer, page, err)
}

func (s *Server) restCalendarICS(writer http.ResponseWriter, request *http.Request) {
	var input eventICSInput
	if !decodeJSON(writer, request, &input) {
		return
	}
	start, err := time.Parse(time.RFC3339, input.Start)
	if err != nil {
		writeError(writer, http.StatusBadRequest, fmt.Errorf("start: %w", err))
		return
	}
	end, err := time.Parse(time.RFC3339, input.End)
	if err != nil {
		writeError(writer, http.StatusBadRequest, fmt.Errorf("end: %w", err))
		return
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
			writeError(writer, http.StatusBadRequest, fmt.Errorf("cancel invitations require id"))
			return
		}
		event, data, err = calendar.GenerateInvitation(event, true)
	default:
		writeError(writer, http.StatusBadRequest, fmt.Errorf("method must be request or cancel"))
		return
	}
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, icsOutput{Event: event, Filename: calendar.Filename(event), MIMEType: "text/calendar", ICS: data})
}

func (s *Server) restCalendarCreate(writer http.ResponseWriter, request *http.Request) {
	s.restCalendarWrite(writer, request, "calendar.create")
}

func (s *Server) restCalendarUpdate(writer http.ResponseWriter, request *http.Request) {
	s.restCalendarWrite(writer, request, "calendar.update")
}

func (s *Server) restCalendarWrite(writer http.ResponseWriter, request *http.Request, kind string) {
	var input eventMutationInput
	if !decodeJSON(writer, request, &input) {
		return
	}
	prepared, err := s.service.PrepareCalendarWrite(request.Context(), input.Connection, kind, input.Event)
	writeResult(writer, prepared, err)
}

func (s *Server) restCalendarDelete(writer http.ResponseWriter, request *http.Request) {
	var input eventDeleteInput
	if !decodeJSON(writer, request, &input) {
		return
	}
	prepared, err := s.service.PrepareCalendarDelete(request.Context(), input.Connection, input.Collection, input.Href, input.ETag, input.RecurrenceID)
	writeResult(writer, prepared, err)
}

func (s *Server) restOperationShow(writer http.ResponseWriter, request *http.Request) {
	var input operationInput
	if !decodeJSON(writer, request, &input) {
		return
	}
	operation, err := s.service.OperationShow(request.Context(), input.Token)
	writeResult(writer, operation, err)
}

func (s *Server) restOperationExecute(writer http.ResponseWriter, request *http.Request) {
	var input operationInput
	if !decodeJSON(writer, request, &input) {
		return
	}
	result, err := s.service.ExecuteOperation(request.Context(), input.Token)
	if err != nil && restExecuteStatus(result, err) != http.StatusOK {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (s *Server) restSync(writer http.ResponseWriter, request *http.Request) {
	var input selectorInput
	if request.Body != nil && request.ContentLength != 0 {
		decoder := json.NewDecoder(io.LimitReader(request.Body, maxMCPHTTPRequestBytes))
		if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
			writeError(writer, http.StatusBadRequest, fmt.Errorf("decode JSON: %w", err))
			return
		}
	}
	result, err := s.service.Sync(request.Context(), input.selector())
	writeResult(writer, result, err)
}

func (s *Server) restCacheStatus(writer http.ResponseWriter, request *http.Request) {
	status, err := s.service.CacheStatus(request.Context())
	writeResult(writer, status, err)
}

func restExecuteStatus(result model.OperationResult, err error) int {
	if err == nil || result.Status == "failed" || result.Status == "uncertain" {
		return http.StatusOK
	}
	return http.StatusBadRequest
}

func decodeJSON(writer http.ResponseWriter, request *http.Request, dest any) bool {
	decoder := json.NewDecoder(io.LimitReader(request.Body, maxMCPHTTPRequestBytes))
	if err := decoder.Decode(dest); err != nil {
		writeError(writer, http.StatusBadRequest, fmt.Errorf("decode JSON: %w", err))
		return false
	}
	return true
}

func writeResult(writer http.ResponseWriter, value any, err error) {
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, value)
}

func writeError(writer http.ResponseWriter, status int, err error) {
	writeJSON(writer, status, map[string]string{"error": err.Error()})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
}

func splitQuery(values []string) []string {
	var result []string
	for _, value := range values {
		for _, item := range strings.Split(value, ",") {
			item = strings.TrimSpace(item)
			if item != "" {
				result = append(result, item)
			}
		}
	}
	return result
}

func encodeBase64(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}
