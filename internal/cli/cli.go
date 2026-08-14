package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/timborovkov/posthouse/internal/calendar"
	postmail "github.com/timborovkov/posthouse/internal/mail"
	"github.com/timborovkov/posthouse/internal/mcpserver"
	"github.com/timborovkov/posthouse/internal/model"
	"github.com/timborovkov/posthouse/internal/service"
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
		return c.connection(args[1:])
	case "mail":
		return c.mail(args[1:])
	case "calendar":
		return c.calendar(ctx, args[1:])
	case "mcp":
		return c.mcp(ctx, args[1:])
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

func (c *CLI) connection(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: posthouse connection <list|add|remove>")
	}
	switch args[0] {
	case "list":
		flags := newSelectorFlags("connection list")
		pageSize := flags.set.Int("page-size", 50, "connections per page, maximum 200")
		cursor := flags.set.String("cursor", "", "opaque continuation cursor")
		if err := flags.set.Parse(args[1:]); err != nil {
			return err
		}
		connections, err := c.service.ListConnections(flags.selector(""), *pageSize, *cursor)
		if err != nil {
			return err
		}
		return writeJSON(c.stdout, connections)
	case "add":
		flags := flag.NewFlagSet("connection add", flag.ContinueOnError)
		flags.SetOutput(c.stderr)
		file := flags.String("file", "-", "connection JSON file, or - for stdin")
		replace := flags.Bool("replace", false, "replace a connection with the same ID")
		if err := flags.Parse(args[1:]); err != nil {
			return err
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
	default:
		return fmt.Errorf("unknown connection command %q", args[0])
	}
}

func (c *CLI) mail(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: posthouse mail <list|search|send>")
	}
	switch args[0] {
	case "list", "search":
		flags := newSelectorFlags("mail " + args[0])
		folder := flags.set.String("folder", "", "IMAP folder; defaults to the connection inbox")
		query := flags.set.String("query", "", "message text query")
		since := flags.set.String("since", "", "inclusive RFC3339 timestamp")
		before := flags.set.String("before", "", "exclusive RFC3339 timestamp")
		unread := flags.set.Bool("unread", false, "only unread messages")
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
		messages, err := c.service.SearchMessages(flags.selector("mail"), postmail.SearchOptions{Folder: *folder, Query: *query, Since: sinceTime, Before: beforeTime, Unread: *unread}, *pageSize, *cursor)
		if err != nil {
			return err
		}
		return writeJSON(c.stdout, messages)
	case "send":
		flags := flag.NewFlagSet("mail send", flag.ContinueOnError)
		flags.SetOutput(c.stderr)
		connection := flags.String("connection", "", "exact connection ID or unique name")
		var to, cc, bcc stringList
		flags.Var(&to, "to", "recipient; repeat or comma-separate")
		flags.Var(&cc, "cc", "CC recipient; repeat or comma-separate")
		flags.Var(&bcc, "bcc", "BCC recipient; repeat or comma-separate")
		subject := flags.String("subject", "", "message subject")
		body := flags.String("body", "", "plain-text body")
		bodyFile := flags.String("body-file", "", "body file, or - for stdin")
		replyTo := flags.String("reply-to", "", "Reply-To address")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *connection == "" || len(to)+len(cc)+len(bcc) == 0 {
			return fmt.Errorf("mail send requires --connection and at least one recipient")
		}
		if *bodyFile != "" {
			data, err := readInput(*bodyFile)
			if err != nil {
				return err
			}
			*body = string(data)
		}
		if err := c.service.SendMessage(model.SendMessage{ConnectionID: *connection, To: to, CC: cc, BCC: bcc, Subject: *subject, Text: *body, ReplyTo: *replyTo}); err != nil {
			return err
		}
		return writeJSON(c.stdout, map[string]bool{"ok": true})
	default:
		return fmt.Errorf("unknown mail command %q", args[0])
	}
}

func (c *CLI) calendar(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: posthouse calendar <list|ics>")
	}
	switch args[0] {
	case "list":
		flags := newSelectorFlags("calendar list")
		start := flags.set.String("start", time.Now().Format(time.RFC3339), "inclusive RFC3339 timestamp")
		end := flags.set.String("end", time.Now().Add(30*24*time.Hour).Format(time.RFC3339), "exclusive RFC3339 timestamp")
		query := flags.set.String("query", "", "text query")
		pageSize := flags.set.Int("page-size", 100, "events per page, maximum 500")
		cursor := flags.set.String("cursor", "", "opaque continuation cursor")
		if err := flags.set.Parse(args[1:]); err != nil {
			return err
		}
		startTime, err := parseOptionalTime(*start)
		if err != nil {
			return fmt.Errorf("start: %w", err)
		}
		endTime, err := parseOptionalTime(*end)
		if err != nil {
			return fmt.Errorf("end: %w", err)
		}
		events, err := c.service.ListEvents(ctx, flags.selector("calendar"), startTime, endTime, *query, *pageSize, *cursor)
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
		var attendees stringList
		flags.Var(&attendees, "attendee", "attendee email; repeat or comma-separate")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *title == "" || *start == "" || *end == "" {
			return fmt.Errorf("calendar ics requires --title, --start, and --end")
		}
		startTime, err := time.Parse(time.RFC3339, *start)
		if err != nil {
			return fmt.Errorf("start: %w", err)
		}
		endTime, err := time.Parse(time.RFC3339, *end)
		if err != nil {
			return fmt.Errorf("end: %w", err)
		}
		event, data, err := c.service.GenerateICS(model.Event{ID: *id, Title: *title, Description: *description, Location: *location, Start: startTime, End: endTime, AllDay: *allDay, Attendees: attendees, Organizer: *organizer})
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
	default:
		return fmt.Errorf("unknown calendar command %q", args[0])
	}
}

func (c *CLI) mcp(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: posthouse mcp <stdio|http>")
	}
	server := mcpserver.New(c.service)
	switch args[0] {
	case "stdio":
		return server.RunStdio(ctx)
	case "http":
		flags := flag.NewFlagSet("mcp http", flag.ContinueOnError)
		flags.SetOutput(c.stderr)
		address := flags.String("address", "127.0.0.1:8791", "listen address")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		logger := slog.New(slog.NewJSONHandler(c.stderr, nil))
		return server.RunHTTP(ctx, *address, os.Getenv("POSTHOUSE_MCP_TOKEN"), logger)
	default:
		return fmt.Errorf("unknown MCP transport %q", args[0])
	}
}

func (c *CLI) tui() error {
	reader := bufio.NewReader(os.Stdin)
	for {
		connections, err := c.service.Connections(model.Selector{})
		if err != nil {
			return err
		}
		_, _ = fmt.Fprint(c.stdout, "\x1b[2J\x1b[HPosthouse\n\n")
		if len(connections) == 0 {
			_, _ = fmt.Fprintln(c.stdout, "No connections. Add one with: posthouse connection add --file connection.json")
		} else {
			for _, connection := range connections {
				capabilities := make([]string, 0, 2)
				if connection.Mail != nil {
					capabilities = append(capabilities, "mail")
				}
				if connection.Calendar != nil {
					capabilities = append(capabilities, "calendar")
				}
				_, _ = fmt.Fprintf(c.stdout, "%-18s %-12s %-18s %s\n", connection.ID, connection.Category, strings.Join(connection.Labels, ","), strings.Join(capabilities, ","))
			}
		}
		_, _ = fmt.Fprint(c.stdout, "\n[r] reload  [q] quit > ")
		line, err := reader.ReadString('\n')
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if strings.EqualFold(strings.TrimSpace(line), "q") {
			return nil
		}
	}
}

func (c *CLI) usage() {
	_, _ = fmt.Fprintln(c.stdout, `Posthouse — one agent-friendly interface for all your mail and calendars

Usage:
  posthouse [--config PATH] connection list|add|remove
  posthouse [--config PATH] mail list|search|send
  posthouse [--config PATH] calendar list|ics
  posthouse [--config PATH] mcp stdio|http
  posthouse [--config PATH] tui

Data commands write JSON except "calendar ics", which writes text/calendar to stdout by default. Run "posthouse <command> -h" for flags.`)
}

type selectorFlags struct {
	set         *flag.FlagSet
	connections stringList
	labels      stringList
	category    *string
}

func newSelectorFlags(name string) *selectorFlags {
	result := &selectorFlags{set: flag.NewFlagSet(name, flag.ContinueOnError)}
	result.set.Var(&result.connections, "connection", "connection ID or name; repeat or comma-separate")
	result.set.Var(&result.labels, "label", "required label; repeat or comma-separate")
	result.category = result.set.String("category", "", "connection category")
	return result
}

func (flags *selectorFlags) selector(capability string) model.Selector {
	return model.Selector{Connections: flags.connections, Labels: flags.labels, Category: *flags.category, Capability: capability}
}

type stringList []string

func (values *stringList) String() string { return strings.Join(*values, ",") }
func (values *stringList) Set(value string) error {
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			*values = append(*values, item)
		}
	}
	return nil
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
	flags := os.O_WRONLY | os.O_CREATE
	if force {
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_EXCL
	}
	file, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("output file %s already exists; pass --force to replace it", path)
		}
		return "", fmt.Errorf("create ICS file: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("secure ICS file: %w", err)
	}
	if _, err := io.WriteString(file, data); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("write ICS file: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close ICS file: %w", err)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return path, nil
	}
	return absolute, nil
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

func SignalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
