package api

import (
	"context"
	"strings"

	"github.com/scalastudios/aether/internal/mail"
	"github.com/scalastudios/aether/internal/secrets"
)

func likePrefix(name string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(name) + "/%"
}

func (s *Server) ensureMailboxIDs(ctx context.Context, account string, names []string) (map[string]string, error) {
	out := map[string]string{}
	if len(names) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `SELECT folder, jmap_id FROM jmap_mailboxes WHERE mailbox_id = $1 AND folder = ANY($2)`, account, names)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var folder, id string
		if err := rows.Scan(&folder, &id); err != nil {
			rows.Close()
			return nil, err
		}
		out[folder] = id
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, name := range names {
		if out[name] != "" {
			continue
		}
		id := "m." + secrets.Token(16)
		err := s.db.QueryRow(ctx, `
			INSERT INTO jmap_mailboxes (mailbox_id, jmap_id, folder) VALUES ($1, $2, $3)
			ON CONFLICT (mailbox_id, folder) DO UPDATE SET folder = EXCLUDED.folder
			RETURNING jmap_id`, account, id, name).Scan(&id)
		if err != nil {
			return nil, err
		}
		out[name] = id
	}
	return out, nil
}

func (s *Server) mailboxFolder(ctx context.Context, account, id string) (string, error) {
	var folder string
	err := s.db.QueryRow(ctx, `SELECT folder FROM jmap_mailboxes WHERE mailbox_id = $1 AND jmap_id = $2`, account, id).Scan(&folder)
	return folder, err
}

func (s *Server) renameJMAPFolders(ctx context.Context, account, from, to string) error {
	prefix := likePrefix(from)
	for _, table := range []string{"jmap_mailboxes", "jmap_messages"} {
		if _, err := s.db.Exec(ctx, `
			UPDATE `+table+` SET folder = $3 || substring(folder FROM char_length($2) + 1)
			WHERE mailbox_id = $1 AND (folder = $2 OR folder LIKE $4 ESCAPE '\')`, account, from, to, prefix); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) deleteJMAPFolder(ctx context.Context, account, folder string) error {
	prefix := likePrefix(folder)
	for _, table := range []string{"jmap_mailboxes", "jmap_messages"} {
		if _, err := s.db.Exec(ctx, `DELETE FROM `+table+` WHERE mailbox_id = $1 AND (folder = $2 OR folder LIKE $3 ESCAPE '\')`, account, folder, prefix); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) ensureEmailID(ctx context.Context, account string, hit mail.MessageHit, messageID string) (string, error) {
	id := "e." + secrets.Token(16)
	err := s.db.QueryRow(ctx, `
		INSERT INTO jmap_messages (mailbox_id, jmap_id, folder, uid_validity, uid, message_id) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (mailbox_id, folder, uid_validity, uid) DO UPDATE
		SET message_id = CASE WHEN jmap_messages.message_id = '' THEN EXCLUDED.message_id ELSE jmap_messages.message_id END
		RETURNING jmap_id`, account, id, hit.Folder, hit.UIDValidity, hit.UID, messageID).Scan(&id)
	return id, err
}

func (s *Server) emailHit(ctx context.Context, account, id string) (mail.MessageHit, string, error) {
	var hit mail.MessageHit
	var messageID string
	err := s.db.QueryRow(ctx, `SELECT folder, uid_validity, uid, message_id FROM jmap_messages WHERE mailbox_id = $1 AND jmap_id = $2`, account, id).
		Scan(&hit.Folder, &hit.UIDValidity, &hit.UID, &messageID)
	return hit, messageID, err
}

func (s *Server) rememberMessageID(ctx context.Context, account, id, messageID string) {
	if messageID == "" {
		return
	}
	_, _ = s.db.Exec(ctx, `UPDATE jmap_messages SET message_id = $3 WHERE mailbox_id = $1 AND jmap_id = $2 AND message_id = ''`, account, id, messageID)
}

func (s *Server) retargetEmail(ctx context.Context, account, id string, hit mail.MessageHit) error {
	_, err := s.db.Exec(ctx, `DELETE FROM jmap_messages WHERE mailbox_id = $1 AND folder = $3 AND uid_validity = $4 AND uid = $5 AND jmap_id <> $2`, account, id, hit.Folder, hit.UIDValidity, hit.UID)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `UPDATE jmap_messages SET folder = $3, uid_validity = $4, uid = $5 WHERE mailbox_id = $1 AND jmap_id = $2`, account, id, hit.Folder, hit.UIDValidity, hit.UID)
	return err
}

func (s *Server) deleteEmailID(ctx context.Context, account, id string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM jmap_messages WHERE mailbox_id = $1 AND jmap_id = $2`, account, id)
	return err
}
