package fetch

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// listDropbox handles file links (/s/, /scl/fi/) and folder links (/sh/,
// /scl/fo/): with dl=1 Dropbox sends the file, or the folder as a zip.
func (c *Client) listDropbox(ctx context.Context, u *url.URL) ([]*File, error) {
	q := u.Query()
	q.Del("raw")
	q.Set("dl", "1")
	u.RawQuery = q.Encode()
	return c.single(ctx, u.String(), func(resp *http.Response) error {
		if isHTML(resp) {
			return fmt.Errorf("Dropbox %s: no file (removed, or the link expired)", redact(u.String()))
		}
		return nil
	})
}

var (
	mediafireFolder = regexp.MustCompile(`/folder/(\w+)`)
	mediafireFile   = regexp.MustCompile(`/(?:file|download|view)/(\w+)`)
)

// listMediaFire handles file pages, which redirect to the file or show a
// download button, and folders, listed through MediaFire's public API.
func (c *Client) listMediaFire(ctx context.Context, u *url.URL) ([]*File, error) {
	if m := mediafireFolder.FindStringSubmatch(u.Path); m != nil {
		return c.listMediaFireFolder(ctx, m[1], "", 0)
	}
	if strings.HasPrefix(u.Host, "download") {
		return c.listDirect(ctx, u)
	}
	dir, err := c.tempDir()
	if err != nil {
		return nil, err
	}
	p, err := c.downloadMediaFire(ctx, u.String(), dir)
	if err != nil {
		return nil, err
	}
	return []*File{{Path: baseName(p), Size: fileSize(p), Local: p}}, nil
}

func (c *Client) downloadMediaFire(ctx context.Context, page, dir string) (string, error) {
	resp, err := c.get(ctx, page)
	if err != nil {
		return "", err
	}
	if !isHTML(resp) {
		return save(resp, dir)
	}
	doc, err := html.Parse(io.LimitReader(resp.Body, 10<<20))
	resp.Body.Close()
	if err != nil {
		return "", err
	}
	var href string
	walk(doc, func(n *html.Node) bool {
		if n.Type == html.ElementNode && n.Data == "a" && attr(n, "id") == "downloadButton" {
			href = attr(n, "href")
			if s := attr(n, "data-scrambled-url"); s != "" {
				if b, err := base64.StdEncoding.DecodeString(s); err == nil {
					href = string(b)
				}
			}
			return false
		}
		return true
	})
	if !strings.HasPrefix(href, "http") {
		return "", fmt.Errorf("MediaFire %s: no download button (file removed?)", page)
	}
	if resp, err = c.get(ctx, href); err != nil {
		return "", err
	}
	if isHTML(resp) {
		resp.Body.Close()
		return "", fmt.Errorf("MediaFire %s: got a page instead of the file", page)
	}
	return save(resp, dir)
}

func (c *Client) listMediaFireFolder(ctx context.Context, key, prefix string, depth int) ([]*File, error) {
	if depth > 4 {
		return nil, nil
	}
	var files []*File
	for _, kind := range []string{"files", "folders"} {
		api := "https://www.mediafire.com/api/1.5/folder/get_content.php?" + url.Values{
			"folder_key": {key}, "content_type": {kind}, "response_format": {"json"}, "chunk_size": {"1000"},
		}.Encode()
		body, err := c.getText(ctx, api)
		if err != nil {
			return nil, err
		}
		var r struct {
			Response struct {
				Result        string `json:"result"`
				Message       string `json:"message"`
				FolderContent struct {
					Files []struct {
						Quickkey string `json:"quickkey"`
						Filename string `json:"filename"`
						Size     string `json:"size"`
					} `json:"files"`
					Folders []struct {
						FolderKey string `json:"folderkey"`
						Name      string `json:"name"`
					} `json:"folders"`
				} `json:"folder_content"`
			} `json:"response"`
		}
		if err := json.Unmarshal([]byte(body), &r); err != nil {
			return nil, fmt.Errorf("MediaFire folder %s: %w", key, err)
		}
		if r.Response.Result != "Success" {
			return nil, fmt.Errorf("MediaFire folder %s: %s", key, r.Response.Message)
		}
		for _, f := range r.Response.FolderContent.Files {
			page := "https://www.mediafire.com/file/" + f.Quickkey
			files = append(files, &File{
				Path: prefix + f.Filename,
				Size: parseSize(f.Size),
				fetch: func(ctx context.Context, dir string) (string, error) {
					return c.downloadMediaFire(ctx, page, dir)
				},
			})
		}
		for _, d := range r.Response.FolderContent.Folders {
			sub, err := c.listMediaFireFolder(ctx, d.FolderKey, prefix+SafeName(d.Name)+"/", depth+1)
			if err != nil {
				return nil, err
			}
			files = append(files, sub...)
		}
	}
	return files, nil
}

func parseSize(s string) int64 {
	var n int64
	if _, err := fmt.Sscan(s, &n); err != nil {
		return -1
	}
	return n
}

func baseName(p string) string { return filepath.Base(p) }

func fileSize(p string) int64 {
	st, err := os.Stat(p)
	if err != nil {
		return -1
	}
	return st.Size()
}
