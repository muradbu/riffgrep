// Package fetch downloads what an Ignition download link points at. Creators
// host their files anywhere: mostly Dropbox and Google Drive (single files,
// folders, archives), sometimes MediaFire or a plain URL.
package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"

// ErrUnsupported is returned for links riffgrep can't download from (a web
// page it doesn't know: OneDrive, Box, ...); open those in a browser.
var ErrUnsupported = errors.New("unsupported download site")

// File is one file a link offers. Lazy files (in a Drive folder listing)
// are only downloaded when wanted; the rest already sit in Local.
type File struct {
	Path  string // within the link: folders and archive names included, "/"-separated
	Size  int64  // -1 when unknown
	Local string // set once downloaded or extracted
	fetch func(ctx context.Context, dir string) (string, error)
}

// Get makes sure the file is on disk, downloading it into dir if needed.
func (f *File) Get(ctx context.Context, dir string) (string, error) {
	if f.Local != "" {
		return f.Local, nil
	}
	p, err := f.fetch(ctx, dir)
	if err != nil {
		return "", err
	}
	f.Local = p
	return p, nil
}

// Client downloads into a temp directory of its own; Close removes it.
type Client struct {
	http *http.Client
	Temp string
}

func NewClient() (*Client, error) {
	jar, _ := cookiejar.New(nil)
	tmp, err := os.MkdirTemp("", "riffgrep-")
	if err != nil {
		return nil, err
	}
	return &Client{
		http: &http.Client{Jar: jar, Timeout: 30 * time.Minute},
		Temp: tmp,
	}, nil
}

func (c *Client) Close() error { return os.RemoveAll(c.Temp) }

// tempDir is a fresh directory under the client's temp dir, so files with
// the same name from different links never collide.
func (c *Client) tempDir() (string, error) {
	return os.MkdirTemp(c.Temp, "dl-")
}

func (c *Client) get(ctx context.Context, u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: HTTP %s", redact(u), resp.Status)
	}
	return resp, nil
}

func (c *Client) getText(ctx context.Context, u string) (string, error) {
	resp, err := c.get(ctx, u)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	return string(b), err
}

func isHTML(resp *http.Response) bool {
	return strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html")
}

// save writes a response body into dir under the name the server gives (or
// the URL's last path segment) and returns its path.
func save(resp *http.Response, dir string) (string, error) {
	defer resp.Body.Close()
	name := responseName(resp)
	dst := filepath.Join(dir, name)
	f, err := os.Create(dst + ".part")
	if err != nil {
		return "", err
	}
	_, err = io.Copy(f, resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dst + ".part")
		return "", err
	}
	return dst, os.Rename(dst+".part", dst)
}

func responseName(resp *http.Response) string {
	name := ""
	if _, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition")); err == nil {
		name = params["filename"]
	}
	if name == "" {
		name, _ = url.PathUnescape(path.Base(resp.Request.URL.Path))
	}
	return SafeName(name)
}

// SafeName makes a file name valid on Windows and macOS alike.
func SafeName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, name)
	name = strings.TrimRight(strings.TrimSpace(name), ". ")
	if name == "" || name == "." || name == ".." {
		name = "download"
	}
	return name
}

// redact drops query strings (Dropbox rlkeys, tokens) from URLs in errors.
func redact(u string) string {
	if i := strings.IndexByte(u, '?'); i >= 0 {
		return u[:i]
	}
	return u
}

// List returns the files a link offers. Archives are not opened yet; see
// Expand.
func (c *Client) List(ctx context.Context, link string) ([]*File, error) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("not a download link: %q", link)
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	switch {
	case host == "drive.google.com" || host == "docs.google.com" || host == "drive.usercontent.google.com":
		return c.listDrive(ctx, u)
	case host == "dropbox.com" || strings.HasSuffix(host, ".dropbox.com") || host == "dl.dropboxusercontent.com":
		return c.listDropbox(ctx, u)
	case host == "mediafire.com" || strings.HasSuffix(host, ".mediafire.com"):
		return c.listMediaFire(ctx, u)
	case host == "mega.nz" || host == "mega.co.nz":
		return c.listMega(ctx, u)
	default:
		return c.listDirect(ctx, u)
	}
}

// A single file: download it now, its name only shows in the response.
func (c *Client) single(ctx context.Context, u string, check func(*http.Response) error) ([]*File, error) {
	resp, err := c.get(ctx, u)
	if err != nil {
		return nil, err
	}
	if check != nil {
		if err := check(resp); err != nil {
			resp.Body.Close()
			return nil, err
		}
	}
	dir, err := c.tempDir()
	if err != nil {
		resp.Body.Close()
		return nil, err
	}
	p, err := save(resp, dir)
	if err != nil {
		return nil, err
	}
	st, _ := os.Stat(p)
	return []*File{{Path: filepath.Base(p), Size: st.Size(), Local: p}}, nil
}

func (c *Client) listDirect(ctx context.Context, u *url.URL) ([]*File, error) {
	return c.single(ctx, u.String(), func(resp *http.Response) error {
		if isHTML(resp) {
			return fmt.Errorf("%w: %s is a web page, not a file", ErrUnsupported, redact(u.String()))
		}
		return nil
	})
}
