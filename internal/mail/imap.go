package mail

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/timborovkov/posthouse/internal/config"
	"github.com/timborovkov/posthouse/internal/model"
	"github.com/timborovkov/posthouse/internal/netproxy"
)

type SearchOptions struct {
	Folder              string
	Query               string
	Since               time.Time
	Before              time.Time
	Unread              bool
	Limit               int
	CursorTime          time.Time
	CursorUID           uint32
	MaxUIDExclusive     uint32
	ExpectedUIDValidity uint32
	Mode                string
}

type SearchResult struct {
	Messages    []model.Message
	UIDValidity uint32
	UIDNext     uint32
	HasMore     bool
}

type searchCandidate struct {
	message model.Message
	size    int64
}

func Search(connection model.Connection, options SearchOptions) (SearchResult, error) {
	return SearchContext(context.Background(), connection, options)
}

func SearchContext(ctx context.Context, connection model.Connection, options SearchOptions) (SearchResult, error) {
	if err := ctx.Err(); err != nil {
		return SearchResult{}, err
	}
	if connection.Mail == nil || connection.Mail.IMAP.Address == "" {
		return SearchResult{}, fmt.Errorf("connection %s has no IMAP capability", connection.ID)
	}
	if options.Limit <= 0 {
		options.Limit = 25
	}
	if options.Limit > 250 {
		return SearchResult{}, fmt.Errorf("limit cannot exceed 250")
	}
	if options.Folder == "" {
		options.Folder = connection.Mail.Folders.Inbox
	}
	if options.Folder == "" {
		options.Folder = "INBOX"
	}
	secret, err := resolvedMailSecret(connection)
	if err != nil {
		return SearchResult{}, err
	}
	client, err := dialIMAPContext(ctx, connection.Mail.IMAP)
	if err != nil {
		return SearchResult{}, err
	}
	defer client.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = client.Close()
		case <-done:
		}
	}()
	if err := client.Login(connection.Mail.Username, secret).Wait(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return SearchResult{}, ctxErr
		}
		return SearchResult{}, fmt.Errorf("authenticate to IMAP: %w", err)
	}
	defer client.Logout()
	selected, err := client.Select(options.Folder, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return SearchResult{}, ctxErr
		}
		return SearchResult{}, fmt.Errorf("select IMAP folder %s: %w", options.Folder, err)
	}
	if options.ExpectedUIDValidity != 0 && selected.UIDValidity != options.ExpectedUIDValidity {
		return SearchResult{}, fmt.Errorf("mailbox UIDVALIDITY changed; restart pagination")
	}
	criteriaSince, criteriaBefore := imapSearchDateBounds(options.Since, options.Before)
	criteria := &imap.SearchCriteria{Since: criteriaSince, Before: criteriaBefore}
	if !options.CursorTime.IsZero() {
		year, month, day := options.CursorTime.Date()
		cursorDayEnd := time.Date(year, month, day+1, 0, 0, 0, 0, options.CursorTime.Location())
		if criteria.Before.IsZero() || cursorDayEnd.Before(criteria.Before) {
			criteria.Before = cursorDayEnd
		}
	}
	if options.Query != "" {
		criteria.Text = []string{options.Query}
	}
	if options.Unread {
		criteria.NotFlag = []imap.Flag{imap.FlagSeen}
	}
	if options.MaxUIDExclusive == 1 {
		return SearchResult{Messages: []model.Message{}, UIDValidity: selected.UIDValidity, UIDNext: uint32(selected.UIDNext)}, nil
	}
	if options.MaxUIDExclusive > 1 {
		criteria.UID = append(criteria.UID, imap.UIDSet{{Start: 1, Stop: imap.UID(options.MaxUIDExclusive - 1)}})
	}
	var uids []imap.UID
	providerOrdered := client.Caps().Has(imap.CapSort)
	if providerOrdered {
		sortedUIDs, sortErr := client.UIDSort(&imapclient.SortOptions{SearchCriteria: criteria, SortCriteria: []imapclient.SortCriterion{{Key: imapclient.SortKeyArrival, Reverse: true}}}).Wait()
		if sortErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return SearchResult{}, ctxErr
			}
			return SearchResult{}, fmt.Errorf("sort IMAP folder: %w", sortErr)
		}
		uids = make([]imap.UID, len(sortedUIDs))
		for index, uid := range sortedUIDs {
			uids[index] = imap.UID(uid)
		}
	} else {
		searchData, searchErr := client.UIDSearch(criteria, nil).Wait()
		if searchErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return SearchResult{}, ctxErr
			}
			return SearchResult{}, fmt.Errorf("search IMAP folder: %w", searchErr)
		}
		uids = searchData.AllUIDs()
	}
	if len(uids) == 0 {
		return SearchResult{Messages: []model.Message{}, UIDValidity: selected.UIDValidity, UIDNext: uint32(selected.UIDNext)}, nil
	}
	if providerOrdered {
		windowLimit := options.Limit + 1
		cursorMissing := missingSortCursor(uids, options.CursorUID)
		if needsUnboundedSortWindow(options) {
			windowLimit = 0
		}
		uids = orderedUIDWindow(uids, options.CursorUID, windowLimit)
		if cursorMissing {
			providerOrdered = false
		}
	}
	candidates, err := fetchSearchCandidates(client, connection.ID, options, uids, providerOrdered)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return SearchResult{}, ctxErr
		}
		return SearchResult{}, err
	}
	hasMore := len(candidates) > options.Limit
	if hasMore {
		candidates = candidates[:options.Limit]
	}
	messages := make([]model.Message, len(candidates))
	sizes := make(map[imap.UID]int64, len(candidates))
	for index, candidate := range candidates {
		messages[index] = candidate.message
		sizes[imap.UID(candidate.message.UID)] = candidate.size
	}
	// Partial BODY responses are implemented inconsistently by otherwise useful
	// IMAP servers. Fetch complete messages only when RFC822.SIZE proves they are
	// small, keeping list reads bounded while retaining useful snippets.
	const previewFetchLimit = 64 << 10
	previewUIDs := make([]imap.UID, 0, len(messages))
	for _, message := range messages {
		if size := sizes[imap.UID(message.UID)]; safePreviewSize(size, previewFetchLimit) {
			previewUIDs = append(previewUIDs, imap.UID(message.UID))
		}
	}
	if len(previewUIDs) > 0 {
		previewSection := &imap.FetchItemBodySection{Peek: true}
		previewItems, previewErr := client.Fetch(imap.UIDSetNum(previewUIDs...), &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{previewSection}}).Collect()
		if previewErr == nil {
			previews := make(map[uint32]string, len(previewItems))
			for _, item := range previewItems {
				previews[uint32(item.UID)] = messagePreview(item.FindBodySection(previewSection))
			}
			for index := range messages {
				messages[index].Preview = previews[messages[index].UID]
			}
		}
	}
	return SearchResult{Messages: messages, UIDValidity: selected.UIDValidity, UIDNext: uint32(selected.UIDNext), HasMore: hasMore}, nil
}

func resolvedMailSecret(connection model.Connection) (string, error) {
	if connection.Mail != nil && connection.Mail.ResolvedSecret != "" {
		return connection.Mail.ResolvedSecret, nil
	}
	return config.ResolveSecret(connection.Mail.Secret)
}

// UnreadCountContext returns the IMAP STATUS UNSEEN count for folder.
func UnreadCountContext(ctx context.Context, connection model.Connection, folder string) (int, error) {
	client, stop, err := authenticatedIMAPContext(ctx, connection)
	if err != nil {
		return 0, err
	}
	defer stop()
	defer client.Close()
	defer client.Logout()
	if folder == "" {
		folder = defaultFolder(connection, folder)
	}
	data, err := client.Status(folder, &imap.StatusOptions{NumUnseen: true}).Wait()
	if err != nil {
		return unreadFromStatus(folder, nil, err)
	}
	return unreadFromStatus(folder, data.NumUnseen, nil)
}

func unreadFromStatus(folder string, unseen *uint32, err error) (int, error) {
	if err != nil {
		return 0, fmt.Errorf("IMAP STATUS UNSEEN for %s: %w", folder, err)
	}
	if unseen == nil {
		return 0, fmt.Errorf("IMAP STATUS UNSEEN for %s returned no count", folder)
	}
	return int(*unseen), nil
}

func safePreviewSize(size, limit int64) bool { return size > 0 && size <= limit }

func missingSortCursor(uids []imap.UID, cursorUID uint32) bool {
	return cursorUID != 0 && !slices.Contains(uids, imap.UID(cursorUID))
}

func needsUnboundedSortWindow(options SearchOptions) bool {
	return needsExactTimestampFilter(options.Since) || !options.Before.IsZero()
}

func orderedUIDWindow(uids []imap.UID, cursorUID uint32, limit int) []imap.UID {
	start := 0
	if cursorUID != 0 {
		start = -1
		for index, uid := range uids {
			if uint32(uid) == cursorUID {
				start = index + 1
				break
			}
		}
		if start < 0 {
			return uids
		}
	}
	end := len(uids)
	if limit > 0 {
		end = min(start+limit, len(uids))
	}
	return uids[start:end]
}

func imapSearchDateBounds(since, before time.Time) (time.Time, time.Time) {
	if !since.IsZero() {
		since = dayStart(since.UTC()).AddDate(0, 0, -1)
	}
	if !before.IsZero() {
		before = dayStart(before.UTC()).AddDate(0, 0, 2)
	}
	return since, before
}

func needsExactTimestampFilter(value time.Time) bool {
	return !value.IsZero() && !value.Equal(dayStart(value))
}

func dayStart(value time.Time) time.Time {
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, value.Location())
}

func fetchSearchCandidates(client *imapclient.Client, connectionID string, options SearchOptions, uids []imap.UID, providerOrdered bool) ([]searchCandidate, error) {
	const fetchChunk = 500
	keep := options.Limit + 1
	candidates := make([]searchCandidate, 0, keep)
	boundary := model.Message{ReceivedAt: options.CursorTime, UID: options.CursorUID}
	for offset := 0; offset < len(uids); offset += fetchChunk {
		end := min(offset+fetchChunk, len(uids))
		items, err := client.Fetch(imap.UIDSetNum(uids[offset:end]...), &imap.FetchOptions{UID: true, Envelope: true, Flags: true, InternalDate: true, RFC822Size: true}).Collect()
		if err != nil {
			return nil, fmt.Errorf("fetch IMAP message metadata: %w", err)
		}
		for _, item := range items {
			if item.Envelope == nil {
				continue
			}
			message := model.Message{
				ConnectionID: connectionID,
				Folder:       options.Folder,
				UID:          uint32(item.UID),
				MessageID:    item.Envelope.MessageID,
				From:         addresses(item.Envelope.From),
				To:           addresses(item.Envelope.To),
				Subject:      item.Envelope.Subject,
				Date:         item.Envelope.Date,
				ReceivedAt:   item.InternalDate,
				Unread:       !slices.Contains(item.Flags, imap.FlagSeen),
				Flagged:      slices.Contains(item.Flags, imap.FlagFlagged),
			}
			when := messageTime(message)
			if !options.Since.IsZero() && when.Before(options.Since) {
				continue
			}
			if !options.Before.IsZero() && !when.Before(options.Before) {
				continue
			}
			if !options.CursorTime.IsZero() && !messageBefore(boundary, message) {
				continue
			}
			candidates = append(candidates, searchCandidate{message: message, size: item.RFC822Size})
		}
		sort.Slice(candidates, func(i, j int) bool { return messageBefore(candidates[i].message, candidates[j].message) })
		if len(candidates) > keep {
			candidates = candidates[:keep]
		}
		if providerOrdered && len(candidates) >= keep {
			break
		}
	}
	return candidates, nil
}

func messageBefore(a, b model.Message) bool {
	aTime, bTime := messageTime(a), messageTime(b)
	if !aTime.Equal(bTime) {
		return aTime.After(bTime)
	}
	// RFC 5256 SORT uses ascending message order as the implicit final
	// tie-breaker. UID order follows mailbox order, so mirror it here to keep
	// provider windows and local aggregate cursors identical.
	return a.UID < b.UID
}

func messageTime(message model.Message) time.Time {
	if !message.ReceivedAt.IsZero() {
		return message.ReceivedAt
	}
	return message.Date
}

func messagePreview(data []byte) string {
	if parsed, err := parseMessage(data); err == nil && parsed.Detail.Preview != "" {
		return parsed.Detail.Preview
	}
	if index := strings.Index(string(data), "\r\n\r\n"); index >= 0 {
		data = data[index+4:]
	} else if index := strings.Index(string(data), "\n\n"); index >= 0 {
		data = data[index+2:]
	}
	return preview(data)
}

func dialIMAP(settings model.IMAPConfig) (*imapclient.Client, error) {
	return dialIMAPContext(context.Background(), settings)
}

func dialIMAPContext(ctx context.Context, settings model.IMAPConfig) (*imapclient.Client, error) {
	var (
		client *imapclient.Client
		err    error
	)
	host, _, splitErr := net.SplitHostPort(settings.Address)
	if splitErr != nil {
		return nil, fmt.Errorf("IMAP address must use host:port: %w", splitErr)
	}
	if !settings.TLS && !settings.StartTLS && host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return nil, fmt.Errorf("refusing remote cleartext IMAP connection; enable tls or starttls")
	}
	connection, err := netproxy.DialTCP(ctx, settings.Address, settings.Proxy)
	if err != nil {
		return nil, fmt.Errorf("connect to IMAP: %w", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	_ = connection.SetDeadline(deadline)
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = connection.Close()
		case <-done:
		}
	}()
	switch {
	case settings.TLS:
		tlsConnection := tls.Client(connection, &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host, NextProtos: []string{"imap"}})
		if err = tlsConnection.HandshakeContext(ctx); err == nil {
			client = imapclient.New(tlsConnection, nil)
		}
	case settings.StartTLS:
		client, err = imapclient.NewStartTLS(connection, &imapclient.Options{TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}})
	default:
		client = imapclient.New(connection, nil)
	}
	if err != nil {
		_ = connection.Close()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("connect to IMAP: %w", err)
	}
	if err := connection.SetDeadline(time.Time{}); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("clear IMAP handshake deadline: %w", err)
	}
	return client, nil
}

func addresses(values []imap.Address) []model.Address {
	result := make([]model.Address, 0, len(values))
	for _, value := range values {
		if value.IsGroupStart() || value.IsGroupEnd() {
			continue
		}
		result = append(result, model.Address{Name: value.Name, Email: value.Addr()})
	}
	return result
}

func preview(value []byte) string {
	text := strings.Join(strings.Fields(string(value)), " ")
	const maxRunes = 400
	runes := []rune(text)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "…"
	}
	return text
}
