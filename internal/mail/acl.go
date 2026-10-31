package mail

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

const (
	SharedPrefix      = "Shared/"
	aclSyncPasses     = 5
	aclRenewEvery     = 30 * time.Second
	aclCommandTimeout = 30 * time.Second
)

var (
	ReadRights = imap.RightSet("lr")
	FullRights = imap.RightSet("lrwstipekx")
)

func SharedFolder(names ...string) bool {
	for _, n := range names {
		if strings.HasPrefix(n+"/", SharedPrefix) {
			return true
		}
	}
	return false
}

type boundedConn struct {
	net.Conn
	until atomic.Int64
}

func (c *boundedConn) extend() {
	c.until.Store(time.Now().Add(aclCommandTimeout).UnixNano())
	c.SetReadDeadline(time.Time{})
}

func (c *boundedConn) clamp(t time.Time) time.Time {
	if until := time.Unix(0, c.until.Load()); t.IsZero() || t.After(until) {
		return until
	}
	return t
}

func (c *boundedConn) SetDeadline(t time.Time) error {
	return errors.Join(c.SetReadDeadline(t), c.SetWriteDeadline(t))
}

func (c *boundedConn) SetReadDeadline(t time.Time) error {
	return c.Conn.SetReadDeadline(c.clamp(t))
}

func (c *boundedConn) SetWriteDeadline(t time.Time) error {
	return c.Conn.SetWriteDeadline(c.clamp(t))
}

func (c *Client) withBounded(ctx context.Context, address string, fn func(*imapclient.Client, *boundedConn) error) error {
	raw, err := (&net.Dialer{Timeout: aclCommandTimeout}).DialContext(ctx, "tcp", c.imapAddr)
	if err != nil {
		return fmt.Errorf("imap connect: %w", err)
	}
	raw.(*net.TCPConn).SetLinger(0)
	conn := &boundedConn{Conn: raw}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	conn.extend()
	ic, err := imapclient.NewStartTLS(conn, &imapclient.Options{TLSConfig: c.tls})
	if err != nil {
		return errors.Join(fmt.Errorf("imap connect: %w", err), ctx.Err())
	}
	if ic, err = c.login(ic, address); err != nil {
		return errors.Join(err, ctx.Err())
	}
	defer ic.Close()
	return errors.Join(fn(ic, conn), ctx.Err())
}

func (c *Client) SyncACL(ctx context.Context, address string, members map[string]imap.RightSet, renew func() error) error {
	var synced []string
	renewed := time.Now()
	hold := func(force bool) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !force && time.Since(renewed) < aclRenewEvery {
			return nil
		}
		if err := renew(); err != nil {
			return err
		}
		renewed = time.Now()
		return nil
	}
	for range aclSyncPasses {
		var stable bool
		err := c.withBounded(ctx, address, func(ic *imapclient.Client, conn *boundedConn) error {
			conn.extend()
			list, err := ic.List("", "*", nil).Collect()
			if err != nil {
				return err
			}
			var names []string
			for _, l := range list {
				if !slices.Contains(l.Attrs, imap.MailboxAttrNoSelect) && !SharedFolder(l.Mailbox) {
					names = append(names, l.Mailbox)
				}
			}
			slices.Sort(names)
			stable = slices.Equal(names, synced)
			if stable {
				return nil
			}
			for _, name := range names {
				if err := hold(false); err != nil {
					return err
				}
				if err := syncFolderACL(ic, conn, name, address, members, hold); err != nil {
					return err
				}
			}
			synced = names
			return nil
		})
		if err != nil || stable {
			return err
		}
	}
	return errors.New("the folder list kept changing while access was being updated")
}

func syncFolderACL(ic *imapclient.Client, conn *boundedConn, name, address string, members map[string]imap.RightSet, hold func(bool) error) error {
	conn.extend()
	acl, err := ic.GetACL(name).Wait()
	if err != nil {
		return err
	}
	for ident := range acl.Rights {
		if _, member := members[string(ident)]; member || string(ident) == address {
			continue
		}
		if err := setACL(ic, conn, name, ident, nil, hold); err != nil {
			return err
		}
	}
	for ident, rights := range members {
		if err := setACL(ic, conn, name, imap.RightsIdentifier(ident), rights, hold); err != nil {
			return err
		}
	}
	return nil
}

func setACL(ic *imapclient.Client, conn *boundedConn, name string, ident imap.RightsIdentifier, rights imap.RightSet, hold func(bool) error) error {
	conn.extend()
	if err := hold(true); err != nil {
		return err
	}
	return ic.SetACL(name, ident, imap.RightModificationReplace, rights).Wait()
}
