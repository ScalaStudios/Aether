package mail

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

type ExportFilter struct {
	Folder string
	Since  time.Time
	Until  time.Time
}

type RawMessage struct {
	Folder   string
	UID      uint32
	Internal time.Time
	Body     []byte
}

func MailExportPath(folder string, uid uint32) (string, error) {
	folder = strings.Trim(folder, "/")
	if folder == "" || strings.Contains(folder, "\\") || SharedFolder(folder) {
		return "", fmt.Errorf("invalid folder")
	}
	for _, part := range strings.Split(folder, "/") {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("invalid folder")
		}
	}
	return "mail/" + folder + "/" + strconv.FormatUint(uint64(uid), 10) + ".eml", nil
}

func WriteMbox(w io.Writer, internal time.Time, raw []byte) error {
	if internal.IsZero() {
		internal = time.Unix(0, 0).UTC()
	}
	if _, err := fmt.Fprintf(w, "From MAILER-DAEMON %s\n", internal.UTC().Format("Mon Jan _2 15:04:05 2006")); err != nil {
		return err
	}
	raw = bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
	raw = bytes.TrimRight(raw, "\n")
	for line := range bytes.SplitSeq(raw, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("From ")) {
			if _, err := w.Write([]byte{'>'}); err != nil {
				return err
			}
		}
		if _, err := w.Write(line); err != nil {
			return err
		}
		if _, err := w.Write([]byte{'\n'}); err != nil {
			return err
		}
	}
	_, err := w.Write([]byte{'\n'})
	return err
}

func (c *Client) ExportMessages(ctx context.Context, address string, filter ExportFilter, emit func(RawMessage) error) error {
	if SharedFolder(filter.Folder) {
		return fmt.Errorf("invalid folder")
	}
	return c.with(address, func(ic *imapclient.Client) error {
		var folders []string
		if filter.Folder != "" {
			folders = []string{filter.Folder}
		} else {
			listed, err := c.Folders(address)
			if err != nil {
				return err
			}
			for _, f := range listed {
				folders = append(folders, f.Name)
			}
		}
		section := &imap.FetchItemBodySection{Peek: true}
		for _, folder := range folders {
			if err := ctx.Err(); err != nil {
				return err
			}
			if _, err := ic.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
				return fmt.Errorf("open %s: %w", folder, err)
			}
			criteria := &imap.SearchCriteria{}
			if !filter.Since.IsZero() {
				criteria.Since = filter.Since.UTC()
			}
			if !filter.Until.IsZero() {
				criteria.Before = filter.Until.UTC().Add(24 * time.Hour)
			}
			found, err := ic.UIDSearch(criteria, nil).Wait()
			if err != nil {
				return fmt.Errorf("search %s: %w", folder, err)
			}
			for _, uid := range found.AllUIDs() {
				if err := ctx.Err(); err != nil {
					return err
				}
				msgs, err := ic.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{
					UID: true, InternalDate: true, BodySection: []*imap.FetchItemBodySection{section},
				}).Collect()
				if err != nil {
					return fmt.Errorf("fetch %s %d: %w", folder, uid, err)
				}
				if len(msgs) == 0 || len(msgs[0].BodySection) == 0 {
					continue
				}
				when := msgs[0].InternalDate.UTC()
				if !filter.Since.IsZero() && when.Before(filter.Since) {
					continue
				}
				if !filter.Until.IsZero() && when.After(filter.Until) {
					continue
				}
				body := msgs[0].FindBodySection(section)
				if len(body) == 0 {
					continue
				}
				if err := emit(RawMessage{Folder: folder, UID: uint32(uid), Internal: when, Body: body}); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
