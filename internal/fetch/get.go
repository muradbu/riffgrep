package fetch

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/muradbu/riffgrep/internal/pick"
)

// Fetch downloads the psarcs a link offers into dest, keeping only the
// preferred variants, and returns the paths it wrote. Archives are unpacked;
// an archive whose name says it's for the other platform is skipped
// without downloading it.
func (c *Client) Fetch(ctx context.Context, link string, prefs pick.Prefs, dest string) ([]string, error) {
	files, err := c.List(ctx, link)
	if err != nil {
		return nil, err
	}
	var psarcs []*File
	var seen []string
	for depth := 0; len(files) > 0 && depth < 3; depth++ {
		var next []*File
		for _, f := range files {
			seen = append(seen, f.Path)
			switch {
			case pick.IsPsarc(f.Path):
				psarcs = append(psarcs, f)
			case IsArchive(f.Path):
				if otherPlatform(f.Path, prefs.Platform) {
					continue
				}
				members, err := c.Expand(ctx, f)
				if err != nil {
					return nil, err
				}
				next = append(next, members...)
			}
		}
		files = next
	}
	if len(psarcs) == 0 {
		return nil, fmt.Errorf("no .psarc files in the download (found: %s)", summary(seen))
	}

	byPath := map[string]*File{}
	paths := make([]string, len(psarcs))
	for i, f := range psarcs {
		byPath[f.Path] = f
		paths[i] = f.Path
	}
	chosen := pick.Choose(paths, prefs)
	if len(chosen) == 0 {
		return nil, fmt.Errorf("no %s files in the download (found: %s)", prefs.Platform, summary(paths))
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return nil, err
	}
	var written []string
	for _, p := range chosen {
		f := byPath[p]
		local, err := f.Get(ctx, c.Temp)
		if err != nil {
			return written, err
		}
		dst := filepath.Join(dest, SafeName(path.Base(p)))
		if err := moveFile(local, dst); err != nil {
			return written, err
		}
		f.Local = ""
		written = append(written, dst)
	}
	return written, nil
}

// otherPlatform: the archive's name (or folder) says it's only for the
// platform we don't want, say "Mac.zip" when downloading for PC.
func otherPlatform(p string, want pick.Platform) bool {
	if want == pick.Unknown {
		return false
	}
	v := pick.Classify(strings.TrimSuffix(p, path.Ext(p)) + "/x.psarc")
	return v.Platform != pick.Unknown && v.Platform != want
}

func summary(names []string) string {
	if len(names) == 0 {
		return "nothing"
	}
	if len(names) > 6 {
		return strings.Join(names[:6], ", ") + fmt.Sprintf(" and %d more", len(names)-6)
	}
	return strings.Join(names, ", ")
}

// moveFile renames, or copies when src and dst are on different volumes
// (the temp dir and a Steam library on another drive).
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst + ".part")
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dst + ".part")
		return err
	}
	if err := os.Rename(dst+".part", dst); err != nil {
		return err
	}
	return os.Remove(src)
}
