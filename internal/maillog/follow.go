package maillog

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type Position struct {
	Inode  uint64
	Offset int64
}

func inode(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino)
	}
	return 0
}

func open(path string, pos Position) (*os.File, Position, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, pos, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, pos, err
	}
	current := Position{Inode: inode(fi)}
	switch {
	case pos.Inode == current.Inode:
		if pos.Offset <= fi.Size() {
			current.Offset = pos.Offset
		}
	default:
		rotated, _ := filepath.Glob(path + ".*")
		for _, name := range rotated {
			if rfi, err := os.Stat(name); err == nil && inode(rfi) == pos.Inode {
				if rf, err := os.Open(name); err == nil {
					f.Close()
					if pos.Offset > rfi.Size() {
						pos.Offset = 0
					}
					return rf, pos, nil
				}
			}
		}
	}
	return f, current, nil
}

func Follow(ctx context.Context, path string, pos Position, handle func(lines []string, pos Position) error) error {
	var f *os.File
	defer func() {
		if f != nil {
			f.Close()
		}
	}()
	buf := make([]byte, 256<<10)
	for ctx.Err() == nil {
		if f == nil {
			var err error
			if f, pos, err = open(path, pos); err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, os.ErrPermission) {
				return err
			}
		}
		read := 0
		if f != nil {
			n, err := f.ReadAt(buf, pos.Offset)
			if err != nil && !errors.Is(err, io.EOF) {
				return err
			}
			if end := bytes.LastIndexByte(buf[:n], '\n'); end >= 0 {
				read = end + 1
			} else if n == len(buf) {
				read = n
			}
		}
		if read > 0 {
			pos.Offset += int64(read)
			lines := bytes.Split(bytes.TrimSuffix(buf[:read], []byte("\n")), []byte("\n"))
			out := make([]string, len(lines))
			for i, l := range lines {
				out[i] = string(l)
			}
			if err := handle(out, pos); err != nil {
				return err
			}
			continue
		}
		if f != nil {
			if fi, err := os.Stat(path); err == nil && inode(fi) != pos.Inode {
				f.Close()
				next := inode(fi)
				rotated, _ := filepath.Glob(path + ".*")
				found := false
				for _, name := range rotated {
					rfi, err := os.Stat(name)
					if err != nil || filepath.Ext(name) == ".gz" {
						continue
					}
					if found {
						next = inode(rfi)
						break
					}
					found = inode(rfi) == pos.Inode
				}
				f, pos = nil, Position{Inode: next}
				continue
			} else if err == nil && fi.Size() < pos.Offset {
				pos.Offset = 0
				continue
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
		}
	}
	return nil
}
