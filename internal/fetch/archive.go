package fetch

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/bodgit/sevenzip"
	"github.com/nwaples/rardecode/v2"

	"github.com/muradbu/riffgrep/internal/pick"
)

// IsArchive says whether a file needs unpacking to get at its psarcs.
func IsArchive(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".zip", ".rar", ".7z":
		return true
	}
	return false
}

// keep says which archive members are worth extracting: psarcs, and
// archives inside the archive.
func keep(name string) bool {
	return pick.IsPsarc(name) || IsArchive(name)
}

// Expand unpacks an archive's psarcs (and nested archives) into a temp dir.
// Member paths are prefixed with the archive's own path, so folder names
// like "Mac" or "No DD" inside it still count when picking.
func (c *Client) Expand(ctx context.Context, f *File) ([]*File, error) {
	local, err := f.Get(ctx, c.Temp)
	if err != nil {
		return nil, err
	}
	dir, err := c.tempDir()
	if err != nil {
		return nil, err
	}
	prefix := strings.TrimSuffix(f.Path, path.Ext(f.Path)) + "/"
	var out []*File
	n := 0
	add := func(name string, r io.Reader) error {
		name = cleanMember(name)
		if name == "" || !keep(name) {
			return nil
		}
		n++
		dst := filepath.Join(dir, fmt.Sprintf("%d-%s", n, SafeName(path.Base(name))))
		w, err := os.Create(dst)
		if err != nil {
			return err
		}
		size, err := io.Copy(w, r)
		if cerr := w.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return fmt.Errorf("%s: %s: %w", f.Path, name, err)
		}
		out = append(out, &File{Path: prefix + name, Size: size, Local: dst})
		return nil
	}

	switch strings.ToLower(path.Ext(f.Path)) {
	case ".zip":
		err = expandZip(local, add)
	case ".rar":
		err = expandRar(local, add)
	case ".7z":
		err = expand7z(local, add)
	default:
		err = fmt.Errorf("%s: not an archive", f.Path)
	}
	if err != nil {
		return nil, fmt.Errorf("unpacking %s: %w", f.Path, err)
	}
	return out, nil
}

// cleanMember turns an archive member name into a relative "/" path, or ""
// for directories and names escaping the archive.
func cleanMember(name string) string {
	name = strings.ReplaceAll(name, `\`, "/")
	name = path.Clean("/" + name)[1:]
	if name == "" || strings.HasSuffix(name, "/") {
		return ""
	}
	return name
}

func expandZip(p string, add func(string, io.Reader) error) error {
	z, err := zip.OpenReader(p)
	if err != nil {
		return err
	}
	defer z.Close()
	for _, zf := range z.File {
		if zf.FileInfo().IsDir() || !keep(zf.Name) {
			continue
		}
		r, err := zf.Open()
		if err != nil {
			return err
		}
		err = add(zf.Name, r)
		r.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func expandRar(p string, add func(string, io.Reader) error) error {
	r, err := rardecode.OpenReader(p)
	if err != nil {
		return err
	}
	defer r.Close()
	for {
		h, err := r.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if h.IsDir || !keep(h.Name) {
			continue
		}
		if err := add(h.Name, r); err != nil {
			return err
		}
	}
}

func expand7z(p string, add func(string, io.Reader) error) error {
	z, err := sevenzip.OpenReader(p)
	if err != nil {
		return err
	}
	defer z.Close()
	for _, zf := range z.File {
		if zf.FileInfo().IsDir() || !keep(zf.Name) {
			continue
		}
		r, err := zf.Open()
		if err != nil {
			return err
		}
		err = add(zf.Name, r)
		r.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
