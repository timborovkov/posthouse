package model

import "time"

type Config struct {
	Version     int          `json:"version"`
	Connections []Connection `json:"connections"`
	Cache       CacheConfig  `json:"cache,omitempty"`
}

type CacheConfig struct {
	Path                string `json:"path,omitempty"`
	MaxBytes            int64  `json:"max_bytes,omitempty"`
	MessageMetadataDays int    `json:"message_metadata_days,omitempty"`
	MessageBodyDays     int    `json:"message_body_days,omitempty"`
	EventPastDays       int    `json:"event_past_days,omitempty"`
	EventFutureDays     int    `json:"event_future_days,omitempty"`
}

type Connection struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Category     string          `json:"category,omitempty"`
	Labels       []string        `json:"labels,omitempty"`
	Identity     Identity        `json:"identity"`
	Mail         *MailConfig     `json:"mail,omitempty"`
	Calendar     *CalendarConfig `json:"calendar,omitempty"`
	Capabilities []string        `json:"capabilities,omitempty"`
	Disabled     bool            `json:"disabled,omitempty"`
}

type Identity struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
}

type MailConfig struct {
	Username  string       `json:"username"`
	Secret    SecretRef    `json:"secret,omitempty"`
	SecretEnv string       `json:"secret_env,omitempty"` // v1 migration only
	IMAP      IMAPConfig   `json:"imap"`
	SMTP      SMTPConfig   `json:"smtp"`
	Folders   FolderConfig `json:"folders,omitempty"`
	SentCopy  string       `json:"sent_copy,omitempty"`
}

type SecretRef struct {
	Env      string `json:"env,omitempty"`
	Keychain string `json:"keychain,omitempty"`
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
	Inbox   string `json:"inbox,omitempty"`
	Sent    string `json:"sent,omitempty"`
	Drafts  string `json:"drafts,omitempty"`
	Archive string `json:"archive,omitempty"`
	Trash   string `json:"trash,omitempty"`
	Junk    string `json:"junk,omitempty"`
}

type CalendarConfig struct {
	Kind         string               `json:"kind,omitempty"`
	URL          string               `json:"url,omitempty"`
	URLSecret    SecretRef            `json:"url_secret,omitempty"`
	URLSecretEnv string               `json:"url_secret_env,omitempty"` // v1 migration only
	Username     string               `json:"username,omitempty"`
	Secret       SecretRef            `json:"secret,omitempty"`
	Collections  []CalendarCollection `json:"collections,omitempty"`
	Insecure     bool                 `json:"insecure,omitempty"`
}

type CalendarCollection struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Path     string `json:"path"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

type Selector struct {
	Connections []string `json:"connections,omitempty"`
	Category    string   `json:"category,omitempty"`
	Labels      []string `json:"labels,omitempty"`
	Capability  string   `json:"capability,omitempty"`
	Collections []string `json:"collections,omitempty"`
}

type ConnectionPage struct {
	Connections []Connection `json:"connections"`
	NextCursor  string       `json:"next_cursor,omitempty"`
}

type Message struct {
	ConnectionID   string    `json:"connection_id"`
	Folder         string    `json:"folder"`
	UID            uint32    `json:"uid"`
	MessageID      string    `json:"message_id,omitempty"`
	From           []Address `json:"from,omitempty"`
	To             []Address `json:"to,omitempty"`
	Subject        string    `json:"subject,omitempty"`
	Date           time.Time `json:"date,omitempty"`
	ReceivedAt     time.Time `json:"received_at,omitempty"`
	Preview        string    `json:"preview,omitempty"`
	Unread         bool      `json:"unread"`
	Flagged        bool      `json:"flagged,omitempty"`
	HasAttachments bool      `json:"has_attachments,omitempty"`
	Stale          bool      `json:"stale,omitempty"`
	CachedAt       time.Time `json:"cached_at,omitempty"`
}

type MessagePage struct {
	Messages   []Message     `json:"messages"`
	NextCursor string        `json:"next_cursor,omitempty"`
	Errors     []SourceError `json:"errors,omitempty"`
}

type SourceError struct {
	ConnectionID string `json:"connection_id"`
	CollectionID string `json:"collection_id,omitempty"`
	Code         string `json:"code"`
	Message      string `json:"message"`
	Retryable    bool   `json:"retryable,omitempty"`
	Stale        bool   `json:"stale,omitempty"`
}

type Address struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email"`
}

type SendMessage struct {
	ConnectionID string            `json:"connection_id"`
	To           []string          `json:"to"`
	CC           []string          `json:"cc,omitempty"`
	BCC          []string          `json:"bcc,omitempty"`
	Subject      string            `json:"subject"`
	Text         string            `json:"text"`
	ReplyTo      string            `json:"reply_to,omitempty"`
	InReplyTo    string            `json:"in_reply_to,omitempty"`
	References   []string          `json:"references,omitempty"`
	Attachments  []AttachmentInput `json:"attachments,omitempty"`
}

type AttachmentInput struct {
	Path        string `json:"path,omitempty"`
	Name        string `json:"name,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Data        []byte `json:"data,omitempty"`
}

type Attachment struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	ContentType string    `json:"content_type,omitempty"`
	Size        int64     `json:"size"`
	Inline      bool      `json:"inline,omitempty"`
	ContentID   string    `json:"content_id,omitempty"`
	Stale       bool      `json:"stale,omitempty"`
	CachedAt    time.Time `json:"cached_at,omitempty"`
}

type MessageDetail struct {
	Message
	CC          []Address    `json:"cc,omitempty"`
	BCC         []Address    `json:"bcc,omitempty"`
	ReplyTo     []Address    `json:"reply_to,omitempty"`
	Text        string       `json:"text,omitempty"`
	HTML        string       `json:"html,omitempty"`
	InReplyTo   string       `json:"in_reply_to,omitempty"`
	References  []string     `json:"references,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

type RecurrencePeriod struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

type Event struct {
	ConnectionID      string             `json:"connection_id,omitempty"`
	ID                string             `json:"id"`
	SeriesID          string             `json:"series_id,omitempty"`
	Title             string             `json:"title"`
	Description       string             `json:"description,omitempty"`
	Location          string             `json:"location,omitempty"`
	Start             time.Time          `json:"start"`
	End               time.Time          `json:"end"`
	AllDay            bool               `json:"all_day,omitempty"`
	Attendees         []string           `json:"attendees,omitempty"`
	Organizer         string             `json:"organizer,omitempty"`
	CollectionID      string             `json:"collection_id,omitempty"`
	ETag              string             `json:"etag,omitempty"`
	Href              string             `json:"href,omitempty"`
	RecurrenceID      string             `json:"recurrence_id,omitempty"`
	RecurrenceRange   string             `json:"recurrence_range,omitempty"`
	RecurrenceRule    string             `json:"recurrence_rule,omitempty"`
	RecurrenceDates   []time.Time        `json:"recurrence_dates,omitempty"`
	RecurrencePeriods []RecurrencePeriod `json:"recurrence_periods,omitempty"`
	ExceptionDates    []time.Time        `json:"exception_dates,omitempty"`
	Sequence          int                `json:"sequence,omitempty"`
	Status            string             `json:"status,omitempty"`
	Stale             bool               `json:"stale,omitempty"`
	CachedAt          time.Time          `json:"cached_at,omitempty"`
}

type EventPage struct {
	Events     []Event       `json:"events"`
	NextCursor string        `json:"next_cursor,omitempty"`
	Errors     []SourceError `json:"errors,omitempty"`
}

type PreparedOperation struct {
	Token        string         `json:"token"`
	Kind         string         `json:"kind"`
	ConnectionID string         `json:"connection_id"`
	Identity     Identity       `json:"identity"`
	Preview      map[string]any `json:"preview"`
	CreatedAt    time.Time      `json:"created_at"`
	ExpiresAt    time.Time      `json:"expires_at"`
	ExecutedAt   time.Time      `json:"executed_at,omitempty"`
	Status       string         `json:"status"`
	Result       map[string]any `json:"result,omitempty"`
}

type OperationResult struct {
	Token      string         `json:"token"`
	Status     string         `json:"status"`
	ExecutedAt time.Time      `json:"executed_at,omitempty"`
	Result     map[string]any `json:"result,omitempty"`
}

type DoctorCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

type DoctorResult struct {
	ConnectionID string        `json:"connection_id"`
	OK           bool          `json:"ok"`
	Checks       []DoctorCheck `json:"checks"`
}
