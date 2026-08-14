package model

import "time"

type Config struct {
	Version     int          `json:"version"`
	Connections []Connection `json:"connections"`
}

type Connection struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Category string          `json:"category,omitempty"`
	Labels   []string        `json:"labels,omitempty"`
	Identity Identity        `json:"identity"`
	Mail     *MailConfig     `json:"mail,omitempty"`
	Calendar *CalendarConfig `json:"calendar,omitempty"`
	Disabled bool            `json:"disabled,omitempty"`
}

type Identity struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
}

type MailConfig struct {
	Username  string       `json:"username"`
	SecretEnv string       `json:"secret_env,omitempty"`
	IMAP      IMAPConfig   `json:"imap"`
	SMTP      SMTPConfig   `json:"smtp"`
	Folders   FolderConfig `json:"folders,omitempty"`
}

type IMAPConfig struct {
	Address  string `json:"address"`
	TLS      bool   `json:"tls"`
	StartTLS bool   `json:"starttls,omitempty"`
	Insecure bool   `json:"insecure,omitempty"`
}

type SMTPConfig struct {
	Address  string `json:"address"`
	TLS      bool   `json:"tls"`
	StartTLS bool   `json:"starttls,omitempty"`
	Insecure bool   `json:"insecure,omitempty"`
}

type FolderConfig struct {
	Inbox string `json:"inbox,omitempty"`
	Sent  string `json:"sent,omitempty"`
}

type CalendarConfig struct {
	URL          string `json:"url,omitempty"`
	URLSecretEnv string `json:"url_secret_env,omitempty"`
}

type Selector struct {
	Connections []string `json:"connections,omitempty"`
	Category    string   `json:"category,omitempty"`
	Labels      []string `json:"labels,omitempty"`
	Capability  string   `json:"capability,omitempty"`
}

type ConnectionPage struct {
	Connections []Connection `json:"connections"`
	NextCursor  string       `json:"next_cursor,omitempty"`
}

type Message struct {
	ConnectionID string    `json:"connection_id"`
	Folder       string    `json:"folder"`
	UID          uint32    `json:"uid"`
	MessageID    string    `json:"message_id,omitempty"`
	From         []Address `json:"from,omitempty"`
	To           []Address `json:"to,omitempty"`
	Subject      string    `json:"subject,omitempty"`
	Date         time.Time `json:"date,omitempty"`
	ReceivedAt   time.Time `json:"received_at,omitempty"`
	Preview      string    `json:"preview,omitempty"`
	Unread       bool      `json:"unread"`
}

type MessagePage struct {
	Messages   []Message `json:"messages"`
	NextCursor string    `json:"next_cursor,omitempty"`
}

type Address struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email"`
}

type SendMessage struct {
	ConnectionID string   `json:"connection_id"`
	To           []string `json:"to"`
	CC           []string `json:"cc,omitempty"`
	BCC          []string `json:"bcc,omitempty"`
	Subject      string   `json:"subject"`
	Text         string   `json:"text"`
	ReplyTo      string   `json:"reply_to,omitempty"`
}

type Event struct {
	ConnectionID string    `json:"connection_id,omitempty"`
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Description  string    `json:"description,omitempty"`
	Location     string    `json:"location,omitempty"`
	Start        time.Time `json:"start"`
	End          time.Time `json:"end"`
	AllDay       bool      `json:"all_day,omitempty"`
	Attendees    []string  `json:"attendees,omitempty"`
	Organizer    string    `json:"organizer,omitempty"`
}

type EventPage struct {
	Events     []Event `json:"events"`
	NextCursor string  `json:"next_cursor,omitempty"`
}
