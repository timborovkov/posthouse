package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/timborovkov/posthouse/internal/calendar"
	"github.com/timborovkov/posthouse/internal/config"
	postmail "github.com/timborovkov/posthouse/internal/mail"
	"github.com/timborovkov/posthouse/internal/mcpserver"
	"github.com/timborovkov/posthouse/internal/model"
	"github.com/timborovkov/posthouse/internal/safeio"
	"github.com/timborovkov/posthouse/internal/service"
	tuiapp "github.com/timborovkov/posthouse/internal/tui"
)

type CLI struct {
	service *service.Service
	stdout  io.Writer
	stderr  io.Writer
}

func New(service *service.Service, stdout, stderr io.Writer) *CLI {
	return &CLI{service: service, stdout: stdout, stderr: stderr}
}

func (c *CLI) Run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		c.usage()
		return nil
	}
	switch args[0] {
	case "help", "-h", "--help":
		c.usage()
		return nil
	case "version":
		_, _ = fmt.Fprintln(c.stdout, mcpserver.Version)
		return nil
	case "config":
		return c.config(args[1:])
	case "connection", "connections":
		return c.connection(ctx, args[1:])
	case "mail":
		return c.mail(ctx, args[1:])
	case "schema":
		return c.schema(args[1:])
	case "calendar":
		return c.calendar(ctx, args[1:])
	case "operation", "operations":
		return c.operation(ctx, args[1:])
	case "cache":
		return c.cache(ctx, args[1:])
	case "sync":
		return c.sync(ctx, args[1:])
	case "mcp":
		return c.mcp(ctx, args[1:])
	case "policy":
		return c.policy(args[1:])
	case "serve":
		return c.serve(ctx, args[1:])
	case "setup":
		return c.setup(args[1:])
	case "skill", "skills":
		return c.skill(args[1:])
	case "tui":
		return c.tui()
	default:
		return fmt.Errorf("unknown command %q; run posthouse help", args[0])
	}
}

func (c *CLI) config(args []string) error {
	if len(args) != 1 || args[0] != "path" {
		return fmt.Errorf("usage: posthouse config path")
	}
	_, err := fmt.Fprintln(c.stdout, c.service.ConfigPath())
	return err
}

func (c *CLI) connection(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: posthouse connection <list|add|update|remove|probe|discover|doctor|auth|secret>")
	}
	switch args[0] {
	case "list":
		flags := newSelectorFlags("connection list")
		capability := flags.set.String("capability", "", "granular capability such as mail.read or calendar.write")
		pageSize := flags.set.Int("page-size", 50, "connections per page, maximum 200")
		cursor := flags.set.String("cursor", "", "opaque continuation cursor")
		if err := flags.set.Parse(args[1:]); err != nil {
			return err
		}
		connections, err := c.service.ListConnections(flags.selector(*capability), *pageSize, *cursor)
		if err != nil {
			return err
		}
		return writeJSON(c.stdout, connections)
	case "probe":
		flags := flag.NewFlagSet("connection probe", flag.ContinueOnError)
		flags.SetOutput(c.stderr)
		email := flags.String("email", "", "identity email used for SRV and autoconfig discovery")
		allowPrivate := flags.Bool("allow-private", false, "allow loopback/private discovered endpoints")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *email == "" && flags.NArg() == 1 {
			*email = flags.Arg(0)
		}
		if *email == "" {
			return fmt.Errorf("usage: posthouse connection probe --email you@example.com")
		}
		result, err := c.service.ProbeConnection(ctx, *email, *allowPrivate)
		if err != nil {
			return err
		}
		return writeJSON(c.stdout, result)
	case "add", "update":
		flags := flag.NewFlagSet("connection add", flag.ContinueOnError)
		flags.SetOutput(c.stderr)
		file := flags.String("file", "", "connection JSON file, or - for stdin")
		kind := flags.String("kind", "", "gmail or microsoft; skips IMAP/SMTP probe")
		email := flags.String("email", "", "identity email; probes IMAP/SMTP/CalDAV unless --kind is set")
		id := flags.String("id", "", "connection id when using --email")
		name := flags.String("name", "", "connection name when using --email")
		category := flags.String("category", "", "connection category when using --email")
		var labels stringList
		flags.Var(&labels, "label", "label when using --email; repeat or comma-separate")
		secretEnv := flags.String("secret-env", "", "environment variable holding the secret when using --email")
		secretKeychain := flags.String("secret-keychain", "", "keychain secret name when using --email")
		var secretCommand argvList
		flags.Var(&secretCommand, "secret-command", "secret command argv; repeat once per argument")
		caldav := flags.Bool("caldav", false, "include discovered CalDAV URL when using --email")
		allowPrivate := flags.Bool("allow-private", false, "allow loopback/private discovered endpoints when using --email")
		replace := flags.Bool("replace", args[0] == "update", "replace a connection with the same ID")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if strings.TrimSpace(*kind) != "" {
			if *file != "" {
				return fmt.Errorf("connection add accepts either --file or --kind, not both")
			}
			connection, err := nativeConnection(*kind, *id, *name, *email, *category)
			if err != nil {
				return err
			}
			if err := c.service.UpsertConnection(connection, *replace); err != nil {
				return err
			}
			return writeJSON(c.stdout, map[string]any{"ok": true, "connection": connection.ID})
		}
		if *email != "" {
			if *file != "" {
				return fmt.Errorf("connection add accepts either --file or --email, not both")
			}
			secret := model.SecretRef{Env: *secretEnv, Keychain: *secretKeychain, Command: secretCommand}
			connection, probe, err := c.service.AddConnectionFromProbe(ctx, *id, *name, *category, *email, labels, secret, *caldav, *replace, *allowPrivate)
			if err != nil {
				return err
			}
			return writeJSON(c.stdout, map[string]any{"ok": true, "connection": connection.ID, "probe": probe})
		}
		if *file == "" {
			*file = "-"
		}
		data, err := readInput(*file)
		if err != nil {
			return err
		}
		var connection model.Connection
		if err := json.Unmarshal(data, &connection); err != nil {
			return fmt.Errorf("decode connection: %w", err)
		}
		if err := c.service.UpsertConnection(connection, *replace); err != nil {
			return err
		}
		return writeJSON(c.stdout, map[string]any{"ok": true, "connection": connection.ID})
	case "remove":
		if len(args) != 2 {
			return fmt.Errorf("usage: posthouse connection remove <id>")
		}
		if err := c.service.RemoveConnection(args[1]); err != nil {
			return err
		}
		return writeJSON(c.stdout, map[string]any{"ok": true, "connection": args[1]})
	case "auth":
		device := false
		id := ""
		for _, arg := range args[1:] {
			switch {
			case arg == "--device":
				device = true
			case strings.HasPrefix(arg, "-"):
				return fmt.Errorf("usage: posthouse connection auth <id> [--device]")
			case id != "":
				return fmt.Errorf("usage: posthouse connection auth <id> [--device]")
			default:
				id = arg
			}
		}
		if id == "" {
			return fmt.Errorf("usage: posthouse connection auth <id> [--device]")
		}
		result, err := c.service.AuthorizeConnection(ctx, id, device)
		if err != nil {
			return err
		}
		return writeJSON(c.stdout, result)
	case "discover":
		if len(args) != 2 {
			return fmt.Errorf("usage: posthouse connection discover <id>")
		}
		connection, err := c.service.DiscoverConnection(ctx, args[1])
		if err != nil {
			return err
		}
		return writeJSON(c.stdout, connection)
	case "doctor":
		if len(args) != 2 {
			return fmt.Errorf("usage: posthouse connection doctor <id>")
		}
		result, err := c.service.DoctorConnection(ctx, args[1])
		if err != nil {
			return err
		}
		return writeJSON(c.stdout, result)
	case "secret":
		if len(args) < 3 || args[1] != "set" {
			return fmt.Errorf("usage: posthouse connection secret set <keychain-name> [--file -]")
		}
		flags := flag.NewFlagSet("connection secret set", flag.ContinueOnError)
		flags.SetOutput(c.stderr)
		file := flags.String("file", "-", "secret input file, or - for stdin")
		if err := flags.Parse(args[3:]); err != nil {
			return err
		}
		data, err := readInput(*file)
		if err != nil {
			return err
		}
		value := strings.TrimRight(string(data), "\r\n")
		if err := config.SetKeychainSecret(args[2], value); err != nil {
			return err
		}
		return writeJSON(c.stdout, map[string]any{"ok": true, "keychain": args[2]})
	default:
		return fmt.Errorf("unknown connection command %q", args[0])
	}
}

func (c *CLI) mail(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: posthouse mail <list|search|triage|unread|get|attachment|send|reply|forward|draft|mark|move|archive|trash|junk>")
	}
	switch args[0] {
	case "list", "search":
		flags := newSelectorFlags("mail " + args[0])
		folder := flags.set.String("folder", "", "IMAP folder; defaults to the connection inbox")
		query := flags.set.String("query", "", "message text query")
		since := flags.set.String("since", "", "inclusive RFC3339 timestamp")
		before := flags.set.String("before", "", "exclusive RFC3339 timestamp")
		unread := flags.set.Bool("unread", false, "only unread messages")
		offline := flags.set.Bool("offline", false, "read only from encrypted cache")
		refresh := flags.set.Bool("refresh", false, "require a live provider refresh without stale fallback")
		pageSize := flags.set.Int("page-size", 25, "messages per page, maximum 100")
		cursor := flags.set.String("cursor", "", "opaque continuation cursor")
		if err := flags.set.Parse(args[1:]); err != nil {
			return err
		}
		if args[0] == "search" && *query == "" {
			return fmt.Errorf("mail search requires --query")
		}
		sinceTime, err := parseOptionalTime(*since)
		if err != nil {
			return fmt.Errorf("since: %w", err)
		}
		beforeTime, err := parseOptionalTime(*before)
		if err != nil {
			return fmt.Errorf("before: %w", err)
		}
		mode, err := readMode(*offline, *refresh)
		if err != nil {
			return err
		}
		messages, err := c.service.SearchMessagesContext(ctx, flags.selector("mail"), postmail.SearchOptions{Folder: *folder, Query: *query, Since: sinceTime, Before: beforeTime, Unread: *unread, Mode: mode}, *pageSize, *cursor)
		if err != nil {
			return err
		}
		return writeJSON(c.stdout, messages)
	case "triage":
		flags := newSelectorFlags("mail triage")
		folder := flags.set.String("folder", "", "IMAP folder; defaults to the connection inbox")
		query := flags.set.String("query", "", "message text query")
		unread := flags.set.Bool("unread", false, "only unread messages")
		pageSize := flags.set.Int("page-size", 25, "messages per page, maximum 100")
		cursor := flags.set.String("cursor", "", "opaque continuation cursor")
		if err := flags.set.Parse(args[1:]); err != nil {
			return err
		}
		page, err := c.service.TriageMessages(ctx, flags.selector("mail"), postmail.SearchOptions{Folder: *folder, Query: *query, Unread: *unread}, *pageSize, *cursor)
		if err != nil {
			return err
		}
		return writeJSON(c.stdout, page)
	case "unread":
		flags := newSelectorFlags("mail unread")
		folder := flags.set.String("folder", "", "IMAP folder; defaults to the connection inbox")
		if err := flags.set.Parse(args[1:]); err != nil {
			return err
		}
		summaries, err := c.service.UnreadCounts(ctx, flags.selector("mail"), *folder)
		if err != nil {
			return err
		}
		return writeJSON(c.stdout, map[string]any{"unread": summaries})
	case "send":
		message, err := c.parseCompose(args[1:], "mail send")
		if err != nil {
			return err
		}
		prepared, err := c.service.PrepareSend(ctx, message)
		if err != nil {
			return err
		}
		return writeJSON(c.stdout, prepared)
	case "get":
		flags := flag.NewFlagSet("mail get", flag.ContinueOnError)
		flags.SetOutput(c.stderr)
		connection := flags.String("connection", "", "exact connection")
		id := flags.String("id", "", "opaque message id from list/search")
		folder := flags.String("folder", "", "mailbox folder; unused when --id encodes it")
		uid := flags.Uint64("uid", 0, "deprecated IMAP-only UID alias for --id")
		offline := flags.Bool("offline", false, "read only from encrypted cache")
		refresh := flags.Bool("refresh", false, "require a live provider read without stale fallback")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		loc, err := parseMessageLocator(*id, *folder, *uid)
		if err != nil {
			return err
		}
		if *connection == "" {
			return fmt.Errorf("mail get requires --connection and --id")
		}
		mode, err := readMode(*offline, *refresh)
		if err != nil {
			return err
		}
		detail, err := c.service.GetMessageModeContext(ctx, *connection, loc, mode)
		if err != nil {
			return err
		}
		return writeJSON(c.stdout, detail)
	case "attachment":
		flags := flag.NewFlagSet("mail attachment", flag.ContinueOnError)
		flags.SetOutput(c.stderr)
		connection := flags.String("connection", "", "exact connection")
		messageID := flags.String("message-id", "", "opaque message id from list/search")
		folder := flags.String("folder", "", "mailbox folder; unused when --id encodes it")
		uid := flags.Uint64("uid", 0, "deprecated IMAP-only UID alias for --message-id")
		id := flags.String("id", "", "attachment ID from mail get")
		output := flags.String("output", "-", "output path, or - for stdout")
		force := flags.Bool("force", false, "replace output file")
		offline := flags.Bool("offline", false, "read only from encrypted cache")
		refresh := flags.Bool("refresh", false, "require a live provider read without stale fallback")
		extractText := flags.Bool("extract-text", false, "for PDF attachments, return extracted plain text instead of raw bytes")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		loc, err := parseMessageLocator(*messageID, *folder, *uid)
		if err != nil {
			return err
		}
		if *connection == "" || *id == "" {
			return fmt.Errorf("mail attachment requires --connection, --message-id or --uid, and --id")
		}
		mode, err := readMode(*offline, *refresh)
		if err != nil {
			return err
		}
		attachment, data, err := c.service.GetAttachmentByLocator(ctx, *connection, loc, *id, mode)
		if err != nil {
			return err
		}
		if *extractText {
			if !strings.Contains(strings.ToLower(attachment.ContentType), "pdf") {
				return fmt.Errorf("extract-text is only supported for PDF attachments")
			}
			text, extractErr := postmail.ExtractPDFText(data)
			if extractErr != nil {
				return extractErr
			}
			if *output == "-" {
				_, err = c.stdout.Write([]byte(text))
				return err
			}
			path, err := safeio.WriteFile(*output, []byte(text), *force)
			if err != nil {
				return err
			}
			return writeJSON(c.stdout, map[string]any{"attachment": attachment, "file": path, "extracted_text": true})
		}
		if *output == "-" {
			_, err = c.stdout.Write(data)
			return err
		}
		path, err := safeio.WriteFile(*output, data, *force)
		if err != nil {
			return err
		}
		return writeJSON(c.stdout, map[string]any{"attachment": attachment, "file": path})
	case "reply", "forward":
		flags := flag.NewFlagSet("mail "+args[0], flag.ContinueOnError)
		flags.SetOutput(c.stderr)
		connection := flags.String("connection", "", "exact connection")
		id := flags.String("id", "", "opaque message id from list/search")
		folder := flags.String("folder", "", "mailbox folder; unused when --id encodes it")
		uid := flags.Uint64("uid", 0, "deprecated IMAP-only UID alias for --id")
		body := flags.String("body", "", "plain-text body to place before the quoted message")
		bodyFile := flags.String("body-file", "", "plain-text body file, or - for stdin")
		htmlBody := flags.String("html", "", "HTML body to place before the quoted message")
		htmlFile := flags.String("html-file", "", "HTML body file, or - for stdin")
		var to stringList
		flags.Var(&to, "to", "forward recipient; repeat or comma-separate")
		verbatim := flags.Bool("verbatim", false, "forward original parts as attachments without quoting the body into the preview")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		loc, err := parseMessageLocator(*id, *folder, *uid)
		if err != nil {
			return err
		}
		if *connection == "" {
			return fmt.Errorf("mail %s requires --connection and --id", args[0])
		}
		if err := loadComposeBodies(body, bodyFile, htmlBody, htmlFile); err != nil {
			return err
		}
		var prepared model.PreparedOperation
		if args[0] == "reply" {
			prepared, err = c.service.PrepareReply(ctx, *connection, loc, *body, *htmlBody)
		} else {
			if len(to) == 0 {
				return fmt.Errorf("mail forward requires at least one --to")
			}
			if *verbatim {
				prepared, err = c.service.PrepareForwardVerbatim(ctx, *connection, loc, to, *body)
			} else {
				prepared, err = c.service.PrepareForward(ctx, *connection, loc, to, *body, *htmlBody)
			}
		}
		if err != nil {
			return err
		}
		return writeJSON(c.stdout, prepared)
	case "mark":
		flags := flag.NewFlagSet("mail mark", flag.ContinueOnError)
		flags.SetOutput(c.stderr)
		connection := flags.String("connection", "", "exact connection")
		id := flags.String("id", "", "opaque message id from list/search")
		folder := flags.String("folder", "", "mailbox folder; unused when --id encodes it")
		uid := flags.Uint64("uid", 0, "deprecated IMAP-only UID alias for --id")
		var uids uidList
		flags.Var(&uids, "uids", "comma-separated UIDs for batch mark")
		read, unread := flags.Bool("read", false, "mark read"), flags.Bool("unread", false, "mark unread")
		flagged, unflagged := flags.Bool("flagged", false, "flag message"), flags.Bool("unflagged", false, "remove flag")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *connection == "" || (*id == "" && *uid == 0 && len(uids) == 0) || (*read && *unread) || (*flagged && *unflagged) || (!*read && !*unread && !*flagged && !*unflagged) {
			return fmt.Errorf("mail mark requires target and one unambiguous state change")
		}
		var seenValue, flagValue *bool
		if *read != *unread {
			value := *read
			seenValue = &value
		}
		if *flagged != *unflagged {
			value := *flagged
			flagValue = &value
		}
		action := service.MailAction{ID: strings.TrimSpace(*id), Folder: *folder, Seen: seenValue, Flagged: flagValue}
		if len(uids) > 0 {
			action.UIDs = []uint32(uids)
		} else if *uid != 0 {
			messageUID, err := checkedUID(*uid)
			if err != nil {
				return err
			}
			action.UID = messageUID
		}
		prepared, err := c.service.PrepareMailAction(ctx, *connection, "mail.mark", action)
		if err != nil {
			return err
		}
		return writeJSON(c.stdout, prepared)
	case "move", "archive", "trash", "junk":
		flags := flag.NewFlagSet("mail "+args[0], flag.ContinueOnError)
		flags.SetOutput(c.stderr)
		connection := flags.String("connection", "", "exact connection")
		id := flags.String("id", "", "opaque message id from list/search")
		folder := flags.String("folder", "", "source folder; unused when --id encodes it")
		uid := flags.Uint64("uid", 0, "deprecated IMAP-only UID alias for --id")
		var uids uidList
		flags.Var(&uids, "uids", "comma-separated UIDs for batch move/mark-style actions")
		destination := flags.String("destination", "", "destination folder for move")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *connection == "" || (*id == "" && *uid == 0 && len(uids) == 0) {
			return fmt.Errorf("mail %s requires --connection and --id, --uid, or --uids", args[0])
		}
		kind := "mail." + args[0]
		action := service.MailAction{ID: strings.TrimSpace(*id), Folder: *folder, Destination: *destination}
		if len(uids) > 0 {
			action.UIDs = []uint32(uids)
		} else if *uid != 0 {
			messageUID, err := checkedUID(*uid)
			if err != nil {
				return err
			}
			action.UID = messageUID
		}
		prepared, err := c.service.PrepareMailAction(ctx, *connection, kind, action)
		if err != nil {
			return err
		}
		return writeJSON(c.stdout, prepared)
	case "draft":
		if len(args) < 2 {
			return fmt.Errorf("usage: posthouse mail draft <create|update|delete>")
		}
		kind := "mail.draft." + args[1]
		flags := flag.NewFlagSet("mail draft "+args[1], flag.ContinueOnError)
		flags.SetOutput(c.stderr)
		connection := flags.String("connection", "", "exact connection")
		id := flags.String("id", "", "opaque draft id from list/search")
		folder := flags.String("folder", "", "drafts folder; unused when --id encodes it")
		uid := flags.Uint64("uid", 0, "deprecated IMAP-only UID alias for --id")
		file := flags.String("file", "", "draft message JSON file, or -")
		if err := flags.Parse(args[2:]); err != nil {
			return err
		}
		if *connection == "" {
			return fmt.Errorf("mail draft requires --connection")
		}
		var message model.SendMessage
		if args[1] != "delete" {
			if *file == "" {
				return fmt.Errorf("mail draft %s requires --file", args[1])
			}
			data, err := readInput(*file)
			if err != nil {
				return err
			}
			if err := json.Unmarshal(data, &message); err != nil {
				return fmt.Errorf("decode draft: %w", err)
			}
		}
		loc := service.MessageLocator{ID: strings.TrimSpace(*id), Folder: *folder}
		if *uid != 0 {
			messageUID, err := checkedUID(*uid)
			if err != nil {
				return err
			}
			loc.UID = messageUID
		}
		if args[1] != "create" && loc.ID == "" && loc.UID == 0 {
			return fmt.Errorf("mail draft %s requires --id", args[1])
		}
		prepared, err := c.service.PrepareDraft(ctx, *connection, kind, loc, message)
		if err != nil {
			return err
		}
		return writeJSON(c.stdout, prepared)
	default:
		return fmt.Errorf("unknown mail command %q", args[0])
	}
}

func (c *CLI) calendar(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: posthouse calendar <list|get|create|update|delete|ics>")
	}
	switch args[0] {
	case "list":
		flags := newSelectorFlags("calendar list")
		defaultStart := time.Now()
		start := flags.set.String("start", defaultStart.Format(time.RFC3339), "inclusive RFC3339 timestamp")
		end := flags.set.String("end", defaultStart.Add(30*24*time.Hour).Format(time.RFC3339), "exclusive RFC3339 timestamp")
		query := flags.set.String("query", "", "text query")
		offline := flags.set.Bool("offline", false, "read only from encrypted cache")
		refresh := flags.set.Bool("refresh", false, "require a live provider refresh without stale fallback")
		pageSize := flags.set.Int("page-size", 100, "events per page, maximum 500")
		cursor := flags.set.String("cursor", "", "opaque continuation cursor")
		if err := flags.set.Parse(args[1:]); err != nil {
			return err
		}
		explicitStart, explicitEnd := false, false
		flags.set.Visit(func(flag *flag.Flag) {
			explicitStart = explicitStart || flag.Name == "start"
			explicitEnd = explicitEnd || flag.Name == "end"
		})
		startTime, endTime, err := calendarListRange(*start, *end, *cursor, explicitStart, explicitEnd)
		if err != nil {
			return err
		}
		mode, err := readMode(*offline, *refresh)
		if err != nil {
			return err
		}
		events, err := c.service.ListEventsMode(ctx, flags.selector("calendar"), startTime, endTime, *query, *pageSize, *cursor, mode)
		if err != nil {
			return err
		}
		return writeJSON(c.stdout, events)
	case "ics":
		flags := flag.NewFlagSet("calendar ics", flag.ContinueOnError)
		flags.SetOutput(c.stderr)
		id := flags.String("id", "", "optional stable event ID")
		title := flags.String("title", "", "event title")
		description := flags.String("description", "", "event description")
		location := flags.String("location", "", "event location")
		start := flags.String("start", "", "RFC3339 timestamp")
		end := flags.String("end", "", "RFC3339 timestamp")
		allDay := flags.Bool("all-day", false, "generate an all-day event")
		organizer := flags.String("organizer", "", "organizer email")
		output := flags.String("output", "-", "output path, or - for stdout")
		force := flags.Bool("force", false, "replace an existing output file")
		method := flags.String("method", "", "optional invitation method: request or cancel")
		sequence := flags.Int64("sequence", 0, "current invitation revision, 0..2147483647")
		var attendees stringList
		flags.Var(&attendees, "attendee", "attendee email; repeat or comma-separate")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *title == "" || *start == "" || *end == "" {
			return fmt.Errorf("calendar ics requires --title, --start, and --end")
		}
		if strings.EqualFold(*method, "cancel") && *id == "" {
			return fmt.Errorf("calendar ics --method cancel requires --id")
		}
		if *sequence < 0 || *sequence > 2147483647 {
			return fmt.Errorf("calendar ics --sequence must be between 0 and 2147483647")
		}
		startTime, err := time.Parse(time.RFC3339, *start)
		if err != nil {
			return fmt.Errorf("start: %w", err)
		}
		endTime, err := time.Parse(time.RFC3339, *end)
		if err != nil {
			return fmt.Errorf("end: %w", err)
		}
		input := model.Event{ID: *id, Title: *title, Description: *description, Location: *location, Start: startTime, End: endTime, AllDay: *allDay, Attendees: attendees, Organizer: *organizer, Sequence: int(*sequence)}
		var event model.Event
		var data string
		if *method == "" {
			event, data, err = c.service.GenerateICS(input)
		} else if strings.EqualFold(*method, "request") || strings.EqualFold(*method, "cancel") {
			event, data, err = calendar.GenerateInvitation(input, strings.EqualFold(*method, "cancel"))
		} else {
			return fmt.Errorf("calendar ics --method must be request or cancel")
		}
		if err != nil {
			return err
		}
		if *output == "-" {
			_, err := io.WriteString(c.stdout, data)
			return err
		}
		path, err := writeICSFile(*output, data, *force)
		if err != nil {
			return err
		}
		return writeJSON(c.stdout, map[string]any{"event": event, "file": path, "filename": calendar.Filename(event), "mime_type": "text/calendar"})
	case "get":
		flags := newSelectorFlags("calendar get")
		id := flags.set.String("id", "", "event or occurrence ID")
		start := flags.set.String("start", time.Now().Add(-90*24*time.Hour).Format(time.RFC3339), "range start")
		end := flags.set.String("end", time.Now().Add(365*24*time.Hour).Format(time.RFC3339), "range end")
		if err := flags.set.Parse(args[1:]); err != nil {
			return err
		}
		if *id == "" {
			return fmt.Errorf("calendar get requires --id")
		}
		startTime, err := time.Parse(time.RFC3339, *start)
		if err != nil {
			return err
		}
		endTime, err := time.Parse(time.RFC3339, *end)
		if err != nil {
			return err
		}
		event, err := c.service.GetEvent(ctx, flags.selector("calendar"), startTime, endTime, *id)
		if err != nil {
			return err
		}
		return writeJSON(c.stdout, event)
	case "create", "update":
		flags := flag.NewFlagSet("calendar "+args[0], flag.ContinueOnError)
		flags.SetOutput(c.stderr)
		connection := flags.String("connection", "", "exact connection")
		file := flags.String("file", "-", "event JSON file, or -")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *connection == "" {
			return fmt.Errorf("calendar %s requires --connection", args[0])
		}
		data, err := readInput(*file)
		if err != nil {
			return err
		}
		var event model.Event
		if err := json.Unmarshal(data, &event); err != nil {
			return fmt.Errorf("decode event: %w", err)
		}
		prepared, err := c.service.PrepareCalendarWrite(ctx, *connection, "calendar."+args[0], event)
		if err != nil {
			return err
		}
		return writeJSON(c.stdout, prepared)
	case "delete":
		flags := flag.NewFlagSet("calendar delete", flag.ContinueOnError)
		flags.SetOutput(c.stderr)
		connection := flags.String("connection", "", "exact connection")
		collection := flags.String("collection", "", "calendar collection ID")
		href := flags.String("href", "", "event href")
		etag := flags.String("etag", "", "event ETag")
		recurrenceID := flags.String("recurrence-id", "", "recurrence ID from calendar list; expanded occurrences cannot be deleted as whole objects")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *connection == "" || *collection == "" || *href == "" || *etag == "" {
			return fmt.Errorf("calendar delete requires --connection, --collection, --href, and --etag")
		}
		prepared, err := c.service.PrepareCalendarDelete(ctx, *connection, *collection, *href, *etag, *recurrenceID)
		if err != nil {
			return err
		}
		return writeJSON(c.stdout, prepared)
	default:
		return fmt.Errorf("unknown calendar command %q", args[0])
	}
}

func calendarListRange(start, end, cursor string, explicitStart, explicitEnd bool) (time.Time, time.Time, error) {
	startTime, err := parseOptionalTime(start)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("start: %w", err)
	}
	endTime, err := parseOptionalTime(end)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("end: %w", err)
	}
	if cursor != "" && !explicitStart {
		startTime = time.Time{}
	}
	if cursor != "" && !explicitEnd {
		endTime = time.Time{}
	}
	return startTime, endTime, nil
}

func (c *CLI) policy(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: posthouse policy <show|deny|allow|mcp-profile>")
	}
	switch args[0] {
	case "show":
		if len(args) != 1 {
			return fmt.Errorf("usage: posthouse policy show")
		}
		status, err := c.service.PolicyStatus()
		if err != nil {
			return err
		}
		return writeJSON(c.stdout, status)
	case "deny", "allow":
		if len(args) < 2 {
			return fmt.Errorf("usage: posthouse policy %s <class>...", args[0])
		}
		var (
			status any
			err    error
		)
		if args[0] == "deny" {
			status, err = c.service.PolicyDeny(args[1:])
		} else {
			status, err = c.service.PolicyAllow(args[1:])
		}
		if err != nil {
			return err
		}
		return writeJSON(c.stdout, status)
	case "mcp-profile":
		if len(args) != 2 {
			return fmt.Errorf("usage: posthouse policy mcp-profile <full|readonly>")
		}
		status, err := c.service.PolicySetMCPProfile(args[1])
		if err != nil {
			return err
		}
		return writeJSON(c.stdout, status)
	default:
		return fmt.Errorf("unknown policy command %q", args[0])
	}
}

func (c *CLI) operation(ctx context.Context, args []string) error {
	if len(args) != 2 || (args[0] != "show" && args[0] != "execute") {
		return fmt.Errorf("usage: posthouse operation <show|execute> <token>")
	}
	if args[0] == "show" {
		operation, err := c.service.OperationShow(ctx, args[1])
		if err != nil {
			return err
		}
		return writeJSON(c.stdout, operation)
	}
	result, err := c.service.ExecuteOperation(ctx, args[1])
	if err != nil {
		_ = writeJSON(c.stdout, result)
		return err
	}
	return writeJSON(c.stdout, result)
}

func (c *CLI) cache(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: posthouse cache <status|clear|rekey>")
	}
	switch args[0] {
	case "status":
		status, err := c.service.CacheStatus(ctx)
		if err != nil {
			return err
		}
		return writeJSON(c.stdout, status)
	case "clear":
		if len(args) != 1 {
			return fmt.Errorf("usage: posthouse cache clear")
		}
		if err := c.service.CacheClear(ctx); err != nil {
			return err
		}
		return writeJSON(c.stdout, map[string]bool{"ok": true})
	case "rekey":
		flags := flag.NewFlagSet("cache rekey", flag.ContinueOnError)
		flags.SetOutput(c.stderr)
		keyEnv := flags.String("key-env", "POSTHOUSE_CACHE_KEY_NEW", "environment variable containing the new encoded key")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		encoded := os.Getenv(*keyEnv)
		if encoded == "" {
			return fmt.Errorf("required new cache key environment variable %s is not set", *keyEnv)
		}
		if err := c.service.CacheRekey(ctx, encoded); err != nil {
			return err
		}
		result := map[string]any{"ok": true}
		if os.Getenv("POSTHOUSE_CACHE_KEY") != "" {
			result["required_action"] = "replace POSTHOUSE_CACHE_KEY with the value from " + *keyEnv + " before starting another Posthouse process"
		}
		return writeJSON(c.stdout, result)
	default:
		return fmt.Errorf("unknown cache command %q", args[0])
	}
}

func (c *CLI) sync(ctx context.Context, args []string) error {
	flags := newSelectorFlags("sync")
	capability := flags.set.String("capability", "", "optional mail or calendar capability")
	if err := flags.set.Parse(args); err != nil {
		return err
	}
	selection := flags.selector(*capability)
	result, err := c.service.Sync(ctx, selection)
	if err != nil {
		return err
	}
	return writeJSON(c.stdout, result)
}

func (c *CLI) parseCompose(args []string, name string) (model.SendMessage, error) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(c.stderr)
	connection := flags.String("connection", "", "exact connection ID or unique name")
	var to, cc, bcc, attachmentPaths stringList
	flags.Var(&to, "to", "recipient; repeat or comma-separate")
	flags.Var(&cc, "cc", "CC recipient; repeat or comma-separate")
	flags.Var(&bcc, "bcc", "BCC recipient; repeat or comma-separate")
	flags.Var(&attachmentPaths, "attachment", "attachment path; repeat")
	subject := flags.String("subject", "", "message subject")
	body := flags.String("body", "", "plain-text body")
	bodyFile := flags.String("body-file", "", "plain-text body file, or - for stdin")
	htmlBody := flags.String("html", "", "HTML body")
	htmlFile := flags.String("html-file", "", "HTML body file, or - for stdin")
	replyTo := flags.String("reply-to", "", "Reply-To address")
	if err := flags.Parse(args); err != nil {
		return model.SendMessage{}, err
	}
	if *connection == "" || len(to)+len(cc)+len(bcc) == 0 {
		return model.SendMessage{}, fmt.Errorf("%s requires --connection and at least one recipient", name)
	}
	if err := loadComposeBodies(body, bodyFile, htmlBody, htmlFile); err != nil {
		return model.SendMessage{}, err
	}
	message := model.SendMessage{ConnectionID: *connection, To: to, CC: cc, BCC: bcc, Subject: *subject, Text: *body, HTML: *htmlBody, ReplyTo: *replyTo}
	for _, path := range attachmentPaths {
		message.Attachments = append(message.Attachments, model.AttachmentInput{Path: path, Name: filepath.Base(path), ContentType: mime.TypeByExtension(filepath.Ext(path))})
	}
	return message, nil
}

func (c *CLI) tui() error {
	return tuiapp.Run(c.service)
}

func (c *CLI) usage() {
	_, _ = fmt.Fprintln(c.stdout, `Posthouse — one local switchboard across your mail and calendars — ready for your agents

Usage:
  posthouse [--config PATH] connection list|add|update|remove|probe|discover|doctor|auth|secret
  posthouse [--config PATH] mail list|search|triage|unread|get|attachment|send|reply|forward|draft|mark|move|archive|trash|junk
  posthouse [--config PATH] calendar list|get|create|update|delete|ics
  posthouse [--config PATH] operation show|execute
  posthouse [--config PATH] policy show|deny|allow|mcp-profile
  posthouse [--config PATH] schema write --dir DIR
  posthouse [--config PATH] sync
  posthouse [--config PATH] cache status|clear|rekey
  posthouse [--config PATH] mcp stdio|http [--profile full|readonly]
  posthouse [--config PATH] serve [--profile full|readonly]
  posthouse [--config PATH] setup
  posthouse [--config PATH] skill list|install
  posthouse [--config PATH] tui

Posthouse is a personal CLI, TUI, MCP server, and REST API — not a hosted SaaS.
All provider writes return a ten-minute prepared token; only "operation execute" performs the side effect.
Default policy allows every class; "policy deny" / POSTHOUSE_POLICY_DENY can block send, move, trash, and other writes.
MCP "readonly" profile (--profile, policy mcp-profile, or POSTHOUSE_MCP_PROFILE) omits prepare/execute tools.
Data commands write JSON except "calendar ics", which writes text/calendar to stdout by default. Run "posthouse <command> -h" for flags.
See GETTING-STARTED.md for a first-run path, or INSTALLATION-AND-USAGE-GUIDE.md for CLI, MCP, REST, and Docker.
Built by Tim Borovkov (https://timb.dev). MIT License.`)
}

type selectorFlags struct {
	set         *flag.FlagSet
	connections stringList
	labels      stringList
	category    *string
	collections stringList
}

func newSelectorFlags(name string) *selectorFlags {
	result := &selectorFlags{set: flag.NewFlagSet(name, flag.ContinueOnError)}
	result.set.Var(&result.connections, "connection", "connection ID or name; repeat or comma-separate")
	result.set.Var(&result.labels, "label", "required label; repeat or comma-separate")
	result.set.Var(&result.collections, "collection", "calendar collection ID or name; repeat or comma-separate")
	result.category = result.set.String("category", "", "connection category")
	return result
}

func (flags *selectorFlags) selector(capability string) model.Selector {
	return model.Selector{Connections: flags.connections, Labels: flags.labels, Category: *flags.category, Capability: capability, Collections: flags.collections}
}

type stringList []string

func (values *stringList) String() string { return strings.Join(*values, ",") }
func (values *stringList) Set(value string) error {
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			*values = append(*values, part)
		}
	}
	return nil
}

type argvList []string

func (values *argvList) String() string { return strings.Join(*values, " ") }
func (values *argvList) Set(value string) error {
	if value == "" {
		return fmt.Errorf("secret command argument is empty")
	}
	*values = append(*values, value)
	return nil
}

type uidList []uint32

func (values *uidList) String() string {
	parts := make([]string, 0, len(*values))
	for _, value := range *values {
		parts = append(parts, fmt.Sprintf("%d", value))
	}
	return strings.Join(parts, ",")
}

func (values *uidList) Set(value string) error {
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		parsed, err := parseUint32(part)
		if err != nil {
			return err
		}
		*values = append(*values, parsed)
	}
	return nil
}

func nativeConnection(kind, id, name, email, category string) (model.Connection, error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind != "gmail" && kind != "microsoft" {
		return model.Connection{}, fmt.Errorf("kind must be gmail or microsoft")
	}
	email = strings.TrimSpace(email)
	if email == "" {
		return model.Connection{}, fmt.Errorf("connection add --kind requires --email")
	}
	if strings.TrimSpace(id) == "" {
		id = kind + "-work"
	}
	if strings.TrimSpace(name) == "" {
		if kind == "gmail" {
			name = "Gmail"
		} else {
			name = "Microsoft"
		}
	}
	if strings.TrimSpace(category) == "" {
		category = "work"
	}
	return model.Connection{
		ID: id, Name: name, Category: category,
		Identity: model.Identity{Email: email},
		Mail:     &model.MailConfig{Kind: kind},
		Calendar: &model.CalendarConfig{Kind: kind},
	}, nil
}

func parseMessageLocator(id, folder string, uid uint64) (service.MessageLocator, error) {
	loc := service.MessageLocator{ID: strings.TrimSpace(id), Folder: folder}
	if uid != 0 {
		parsed, err := checkedUID(uid)
		if err != nil {
			return service.MessageLocator{}, err
		}
		loc.UID = parsed
	}
	if loc.ID == "" && loc.UID == 0 {
		return service.MessageLocator{}, fmt.Errorf("message --id is required (deprecated IMAP --uid is still accepted)")
	}
	return loc, nil
}

func parseUint32(value string) (uint32, error) {
	var parsed uint64
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("invalid uid %q", value)
		}
		parsed = parsed*10 + uint64(r-'0')
		if parsed > uint64(^uint32(0)) {
			return 0, fmt.Errorf("uid %q out of range", value)
		}
	}
	if parsed == 0 {
		return 0, fmt.Errorf("uid must be positive")
	}
	return uint32(parsed), nil
}

func readInput(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return data, nil
}

func writeICSFile(path string, data string, force bool) (string, error) {
	return safeio.WriteFile(path, []byte(data), force)
}

func loadComposeBodies(body, bodyFile, htmlBody, htmlFile *string) error {
	if *bodyFile != "" && *htmlFile != "" && *bodyFile == "-" && *htmlFile == "-" {
		return fmt.Errorf("--body-file and --html-file cannot both read stdin")
	}
	if *bodyFile != "" {
		data, err := readInput(*bodyFile)
		if err != nil {
			return err
		}
		*body = string(data)
	}
	if *htmlFile != "" {
		data, err := readInput(*htmlFile)
		if err != nil {
			return err
		}
		*htmlBody = string(data)
	}
	return nil
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func parseOptionalTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, value)
}

func checkedUID(value uint64) (uint32, error) {
	if value > uint64(^uint32(0)) {
		return 0, fmt.Errorf("message UID %d exceeds the uint32 IMAP range", value)
	}
	return uint32(value), nil
}

func readMode(offline, refresh bool) (string, error) {
	if offline && refresh {
		return "", fmt.Errorf("--offline and --refresh cannot be used together")
	}
	if offline {
		return "offline", nil
	}
	if refresh {
		return "refresh", nil
	}
	return "", nil
}

func SignalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
