package mail

import (
	"context"
	"slices"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

type ImportFolder struct {
	Source, Target                 string
	SourceValidity, TargetValidity uint32
}

type ImportSync struct {
	Flags, Deletions bool
	Load             func(context.Context, ImportFolder) (map[imap.UID]imap.UID, error)
	Save             func(context.Context, ImportFolder, imap.UID, imap.UID) error
}

func (s *ImportSync) update(to *imapclient.Client, uid imap.UID, target, source []imap.Flag, progress *ImportProgress) error {
	if s.Deletions && slices.Contains(source, imap.FlagDeleted) {
		return deleteImportCopy(to, uid, progress)
	}
	if !s.Flags {
		return nil
	}
	updated := false
	for _, op := range []imap.StoreFlagsOp{imap.StoreFlagsAdd, imap.StoreFlagsDel} {
		var flags []imap.Flag
		for _, flag := range []imap.Flag{imap.FlagSeen, imap.FlagAnswered, imap.FlagFlagged, imap.FlagDraft} {
			has := func(list []imap.Flag) bool {
				return slices.ContainsFunc(list, func(f imap.Flag) bool { return strings.EqualFold(string(f), string(flag)) })
			}
			if (op == imap.StoreFlagsAdd && has(source) && !has(target)) || (op == imap.StoreFlagsDel && has(target) && !has(source)) {
				flags = append(flags, flag)
			}
		}
		if len(flags) > 0 {
			if err := to.Store(imap.UIDSetNum(uid), &imap.StoreFlags{Op: op, Silent: true, Flags: flags}, nil).Close(); err != nil {
				return err
			}
			updated = true
		}
	}
	if updated {
		progress.FlagsUpdated++
	}
	return nil
}

func deleteImportCopy(to *imapclient.Client, uid imap.UID, progress *ImportProgress) error {
	set := imap.UIDSetNum(uid)
	if err := to.Store(set, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close(); err != nil {
		return err
	}
	if err := to.UIDExpunge(set).Close(); err != nil {
		return err
	}
	progress.Deleted++
	return nil
}
