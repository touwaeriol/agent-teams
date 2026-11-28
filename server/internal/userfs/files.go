package userfs

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/touwaeriol/agent-teams/server/internal/msg"
)

// The console reads and writes a volume (the project's 共享文件 page, importing old shared files)
// without mounting it: Files is a volume's tree, paths relative to its root with "/" separators.

// Entry is one file or folder.
type Entry struct {
	Name    string    `json:"name"`
	Dir     bool      `json:"dir"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

// Files is a volume's tree.
type Files interface {
	// List returns the entries of a folder, folders first, then by name (ErrNoFile when missing).
	List(ctx context.Context, dir string) ([]Entry, error)
	// Open reads a file (ErrNoFile when missing or a folder).
	Open(ctx context.Context, name string) (io.ReadCloser, Entry, error)
	// Put writes a file of size bytes, creating its folders and replacing what was there.
	Put(ctx context.Context, name string, r io.Reader, size int64) error
	// Mkdir creates a folder and its parents.
	Mkdir(ctx context.Context, dir string) error
	// Remove deletes a file, or a folder with everything in it (ErrNoFile when missing).
	Remove(ctx context.Context, name string) error
}

var (
	// ErrNoFile is a path with nothing at it.
	ErrNoFile = msg.Err("没有这个文件或目录")
	// ErrBadPath is a path that is not a clean relative path.
	ErrBadPath = msg.Err("路径不合法")
)

// MaxPathBytes bounds a path inside a volume.
const MaxPathBytes = 1024

// CleanPath checks a path inside a volume and returns it without leading or trailing "/" ("" is
// the root). It refuses "..", ".", empty segments, control characters and invalid UTF-8.
func CleanPath(p string) (string, error) {
	p = strings.Trim(p, "/")
	if p == "" {
		return "", nil
	}
	if len(p) > MaxPathBytes || !utf8.ValidString(p) || strings.ContainsAny(p, "\\\x00") {
		return "", ErrBadPath
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.IndexFunc(seg, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
			return "", ErrBadPath
		}
	}
	return p, nil
}

// Join is CleanPath of base/rel, refusing a rel that leaves base.
func Join(base, rel string) (string, error) {
	r, err := CleanPath(rel)
	if err != nil {
		return "", err
	}
	return CleanPath(path.Join(base, r))
}

func sortEntries(es []Entry) {
	sort.Slice(es, func(i, j int) bool {
		if es[i].Dir != es[j].Dir {
			return es[i].Dir
		}
		return es[i].Name < es[j].Name
	})
}

// DirFiles is Files on a local folder (tests, and a node-local stand-in).
type DirFiles struct{ Root string }

func (d DirFiles) abs(name string) (string, error) {
	p, err := CleanPath(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(d.Root, filepath.FromSlash(p)), nil
}

func noFile(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNoFile
	}
	return err
}

// List implements Files.
func (d DirFiles) List(_ context.Context, dir string) ([]Entry, error) {
	p, err := d.abs(dir)
	if err != nil {
		return nil, err
	}
	des, err := os.ReadDir(p)
	if err != nil {
		return nil, noFile(err)
	}
	out := make([]Entry, 0, len(des))
	for _, de := range des {
		info, err := de.Info()
		if err != nil {
			continue
		}
		e := Entry{Name: de.Name(), Dir: de.IsDir(), ModTime: info.ModTime()}
		if !e.Dir {
			e.Size = info.Size()
		}
		out = append(out, e)
	}
	sortEntries(out)
	return out, nil
}

// Open implements Files.
func (d DirFiles) Open(_ context.Context, name string) (io.ReadCloser, Entry, error) {
	p, err := d.abs(name)
	if err != nil {
		return nil, Entry{}, err
	}
	info, err := os.Stat(p)
	if err != nil || info.IsDir() {
		return nil, Entry{}, ErrNoFile
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, Entry{}, noFile(err)
	}
	return f, Entry{Name: info.Name(), Size: info.Size(), ModTime: info.ModTime()}, nil
}

// Put implements Files.
func (d DirFiles) Put(_ context.Context, name string, r io.Reader, _ int64) error {
	p, err := d.abs(name)
	if err != nil || p == d.Root {
		return ErrBadPath
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.Create(p)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Mkdir implements Files.
func (d DirFiles) Mkdir(_ context.Context, dir string) error {
	p, err := d.abs(dir)
	if err != nil {
		return err
	}
	return os.MkdirAll(p, 0o755)
}

// Remove implements Files.
func (d DirFiles) Remove(_ context.Context, name string) error {
	p, err := d.abs(name)
	if err != nil || p == d.Root {
		return ErrBadPath
	}
	if _, err := os.Lstat(p); err != nil {
		return noFile(err)
	}
	return os.RemoveAll(p)
}
