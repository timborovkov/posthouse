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
	"github.com/posthousehq/posthouse/internal/config"
	"github.com/posthousehq/posthouse/internal/model"
)

type SearchOptions struct {
	Folder              string
	Query               string
	Since               time.Time
	Before              time.Time
	Unread              bool
	Limit               int
	BeforeUID           uint32
	ExpectedUIDValidity uint32
}

type SearchResult struct {
	Messages    []model.Message
	UIDValidity uint32
	UIDNext     uint32
	HasMore     bool
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
	secret, err := config.Secret(connection.Mail.SecretEnv)
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
	if options.BeforeUID == 1 {
		return SearchResult{Messages: []model.Message{}, UIDValidity: selected.UIDValidity, UIDNext: uint32(selected.UIDNext)}, nil
	}

	criteria := &imap.SearchCriteria{Since: options.Since, Before: options.Before}
	if options.BeforeUID > 1 {
		var uidSet imap.UIDSet
		uidSet.AddRange(1, imap.UID(options.BeforeUID-1))
		criteria.UID = []imap.UIDSet{uidSet}
	}
	if options.Query != "" {
		criteria.Text = []string{options.Query}
	}
	if options.Unread {
		criteria.NotFlag = []imap.Flag{imap.FlagSeen}
	}
	searchData, err := client.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return SearchResult{}, fmt.Errorf("search IMAP folder: %w", err)
	}
	uids := searchData.AllUIDs()
	if len(uids) == 0 {
		return SearchResult{Messages: []model.Message{}, UIDValidity: selected.UIDValidity, UIDNext: uint32(selected.UIDNext)}, nil
	}
	hasMore := len(uids) > options.Limit
	if len(uids) > options.Limit {
		uids = uids[len(uids)-options.Limit:]
	}
	set := imap.UIDSetNum(uids...)
	previewSection := &imap.FetchItemBodySection{Specifier: imap.PartSpecifierText, Peek: true, Partial: &imap.SectionPartial{Size: 2048}}
	fetched, err := client.Fetch(set, &imap.FetchOptions{UID: true, Envelope: true, Flags: true, InternalDate: true, BodySection: []*imap.FetchItemBodySection{previewSection}}).Collect()
	if err != nil {
		return SearchResult{}, fmt.Errorf("fetch IMAP messages: %w", err)
	}
	messages := make([]model.Message, 0, len(fetched))
	for _, item := range fetched {
		if item.Envelope == nil {
			continue
		}
		messages = append(messages, model.Message{
			ConnectionID: connection.ID,
			Folder:       options.Folder,
			UID:          uint32(item.UID),
			MessageID:    item.Envelope.MessageID,
			From:         addresses(item.Envelope.From),
			To:           addresses(item.Envelope.To),
			Subject:      item.Envelope.Subject,
			Date:         item.Envelope.Date,
			ReceivedAt:   item.InternalDate,
			Preview:      preview(item.FindBodySection(previewSection)),
			Unread:       !slices.Contains(item.Flags, imap.FlagSeen),
		})
	}
	sort.Slice(messages, func(i, j int) bool { return messages[i].UID > messages[j].UID })
	return SearchResult{Messages: messages, UIDValidity: selected.UIDValidity, UIDNext: uint32(selected.UIDNext), HasMore: hasMore}, nil
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
