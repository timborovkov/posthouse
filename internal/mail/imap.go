package mail

import (
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
	secret, err := config.ResolveSecret(connection.Mail.Secret)
	if err != nil {
		return SearchResult{}, err
	}
	client, err := dialIMAP(connection.Mail.IMAP)
	if err != nil {
		return SearchResult{}, err
	}
	defer client.Close()
	if err := client.Login(connection.Mail.Username, secret).Wait(); err != nil {
		return SearchResult{}, fmt.Errorf("authenticate to IMAP: %w", err)
	}
	defer client.Logout()
	selected, err := client.Select(options.Folder, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return SearchResult{}, fmt.Errorf("select IMAP folder %s: %w", options.Folder, err)
	}
	if options.ExpectedUIDValidity != 0 && selected.UIDValidity != options.ExpectedUIDValidity {
		return SearchResult{}, fmt.Errorf("mailbox UIDVALIDITY changed; restart pagination")
	}
	criteria := &imap.SearchCriteria{Since: options.Since, Before: options.Before}
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
			return SearchResult{}, fmt.Errorf("sort IMAP folder: %w", sortErr)
		}
		uids = make([]imap.UID, len(sortedUIDs))
		for index, uid := range sortedUIDs {
			uids[index] = imap.UID(uid)
		}
	} else {
		searchData, searchErr := client.UIDSearch(criteria, nil).Wait()
		if searchErr != nil {
			return SearchResult{}, fmt.Errorf("search IMAP folder: %w", searchErr)
		}
		uids = searchData.AllUIDs()
	}
	if len(uids) == 0 {
		return SearchResult{Messages: []model.Message{}, UIDValidity: selected.UIDValidity, UIDNext: uint32(selected.UIDNext)}, nil
	}
	candidates, err := fetchSearchCandidates(client, connection.ID, options, uids, providerOrdered)
	if err != nil {
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
		if size := sizes[imap.UID(message.UID)]; size >= 0 && size <= previewFetchLimit {
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
	return a.UID > b.UID
}

func messageTime(message model.Message) time.Time {
	if !message.ReceivedAt.IsZero() {
		return message.ReceivedAt
	}
	return message.Date
}

func messagePreview(data []byte) string {
	if index := strings.Index(string(data), "\r\n\r\n"); index >= 0 {
		data = data[index+4:]
	} else if index := strings.Index(string(data), "\n\n"); index >= 0 {
		data = data[index+2:]
	}
	return preview(data)
}

func dialIMAP(settings model.IMAPConfig) (*imapclient.Client, error) {
	var (
		client *imapclient.Client
		err    error
	)
	switch {
	case settings.TLS:
		client, err = imapclient.DialTLS(settings.Address, nil)
	case settings.StartTLS:
		client, err = imapclient.DialStartTLS(settings.Address, nil)
	default:
		host, _, splitErr := net.SplitHostPort(settings.Address)
		if splitErr != nil {
			return nil, fmt.Errorf("IMAP address must use host:port: %w", splitErr)
		}
		if !settings.Insecure && host != "localhost" && host != "127.0.0.1" && host != "::1" {
			return nil, fmt.Errorf("refusing cleartext IMAP connection without insecure=true")
		}
		client, err = imapclient.DialInsecure(settings.Address, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("connect to IMAP: %w", err)
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
