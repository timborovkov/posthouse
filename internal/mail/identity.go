package mail

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/timborovkov/posthouse/internal/model"
)

const imapIDPrefix = "imap:"

// IMAPLocator is the mailbox address encoded inside an opaque IMAP message id.
type IMAPLocator struct {
	Folder      string
	UIDValidity uint32
	UID         uint32
}

// EncodeIMAPID builds a provider-opaque id that round-trips folder, UIDVALIDITY, and UID.
func EncodeIMAPID(folder string, uidValidity, uid uint32) string {
	return imapIDPrefix + strconv.FormatUint(uint64(uidValidity), 10) + ":" + strconv.FormatUint(uint64(uid), 10) + ":" + base64.RawURLEncoding.EncodeToString([]byte(folder))
}

// ParseIMAPID decodes an IMAP-backend message id. Non-IMAP ids return false.
func ParseIMAPID(id string) (IMAPLocator, bool, error) {
	id = strings.TrimSpace(id)
	if id == "" || !strings.HasPrefix(id, imapIDPrefix) {
		return IMAPLocator{}, false, nil
	}
	rest := strings.TrimPrefix(id, imapIDPrefix)
	uidValidityText, rest, ok := strings.Cut(rest, ":")
	if !ok {
		return IMAPLocator{}, true, fmt.Errorf("invalid IMAP message id")
	}
	uidText, encodedFolder, ok := strings.Cut(rest, ":")
	if !ok {
		return IMAPLocator{}, true, fmt.Errorf("invalid IMAP message id")
	}
	uidValidity, err := strconv.ParseUint(uidValidityText, 10, 32)
	if err != nil {
		return IMAPLocator{}, true, fmt.Errorf("invalid IMAP message id")
	}
	uid, err := strconv.ParseUint(uidText, 10, 32)
	if err != nil || uid == 0 {
		return IMAPLocator{}, true, fmt.Errorf("invalid IMAP message id")
	}
	folder, err := base64.RawURLEncoding.DecodeString(encodedFolder)
	if err != nil || len(folder) == 0 {
		return IMAPLocator{}, true, fmt.Errorf("invalid IMAP message id")
	}
	return IMAPLocator{Folder: string(folder), UIDValidity: uint32(uidValidity), UID: uint32(uid)}, true, nil
}

// StampIMAPMessages fills opaque ids for IMAP search/get results that still only have UID metadata.
func StampIMAPMessages(messages []model.Message, folder string, uidValidity uint32) {
	for index := range messages {
		if messages[index].Folder == "" {
			messages[index].Folder = folder
		}
		if messages[index].ID == "" && messages[index].UID != 0 {
			messages[index].ID = EncodeIMAPID(messages[index].Folder, uidValidity, messages[index].UID)
		}
	}
}

func StampIMAPMessage(message *model.Message, folder string, uidValidity uint32) {
	if message == nil {
		return
	}
	if message.Folder == "" {
		message.Folder = folder
	}
	if message.ID == "" && message.UID != 0 {
		message.ID = EncodeIMAPID(message.Folder, uidValidity, message.UID)
	}
}
