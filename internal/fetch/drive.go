package fetch

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

var (
	driveFileID   = regexp.MustCompile(`/(?:file/d|uc/d)/([\w-]{10,})`)
	driveFolderID = regexp.MustCompile(`/folders/([\w-]{10,})`)
)

// listDrive handles drive.google.com and docs.google.com links: files
// (/file/d/<id>, open?id=, uc?id=) and folders, which are listed without
// downloading anything.
func (c *Client) listDrive(ctx context.Context, u *url.URL) ([]*File, error) {
	q := u.Query()
	rk := q.Get("resourcekey")
	if m := driveFolderID.FindStringSubmatch(u.Path); m != nil {
		return c.listDriveFolder(ctx, m[1], rk, "", 0)
	}
	if strings.Contains(u.Path, "folderview") && q.Get("id") != "" {
		return c.listDriveFolder(ctx, q.Get("id"), rk, "", 0)
	}
	id := q.Get("id")
	if m := driveFileID.FindStringSubmatch(u.Path); m != nil {
		id = m[1]
	}
	if id == "" {
		return nil, fmt.Errorf("%w: Google link without a file or folder ID (%s)", ErrUnsupported, redact(u.String()))
	}
	dir, err := c.tempDir()
	if err != nil {
		return nil, err
	}
	p, err := c.downloadDrive(ctx, id, rk, dir)
	if err != nil {
		return nil, err
	}
	return []*File{{Path: baseName(p), Size: fileSize(p), Local: p}}, nil
}

func (c *Client) downloadDrive(ctx context.Context, id, rk, dir string) (string, error) {
	v := url.Values{"id": {id}, "export": {"download"}, "confirm": {"t"}}
	if rk != "" {
		v.Set("resourcekey", rk)
	}
	resp, err := c.get(ctx, "https://drive.usercontent.google.com/download?"+v.Encode())
	if err != nil {
		return "", err
	}
	if isHTML(resp) {
		// The "can't scan this file for viruses" page: its form downloads
		// anyway. Anything else (quota exceeded, no access) is an error.
		next, msg := driveConfirm(resp)
		if next == "" {
			return "", fmt.Errorf("Google Drive file %s: %s", id, msg)
		}
		if resp, err = c.get(ctx, next); err != nil {
			return "", err
		}
		if isHTML(resp) {
			_, msg := driveConfirm(resp)
			return "", fmt.Errorf("Google Drive file %s: %s", id, msg)
		}
	}
	return save(resp, dir)
}

// driveConfirm reads a Drive interstitial page: the download form's URL if
// there is one, else the page's message.
func driveConfirm(resp *http.Response) (next, msg string) {
	defer resp.Body.Close()
	doc, err := html.Parse(io.LimitReader(resp.Body, 5<<20))
	if err != nil {
		return "", "unreadable page"
	}
	var form *html.Node
	var title string
	walk(doc, func(n *html.Node) bool {
		if n.Type != html.ElementNode {
			return true
		}
		switch n.Data {
		case "form":
			if attr(n, "id") == "download-form" {
				form = n
			}
		case "title":
			title = text(n)
		}
		return true
	})
	if form == nil {
		if title == "" {
			title = "no download on this page (quota exceeded or not shared?)"
		}
		return "", title
	}
	v := url.Values{}
	walk(form, func(n *html.Node) bool {
		if n.Type == html.ElementNode && n.Data == "input" && attr(n, "name") != "" {
			v.Set(attr(n, "name"), attr(n, "value"))
		}
		return true
	})
	action, err := resp.Request.URL.Parse(attr(form, "action"))
	if err != nil {
		return "", "bad download form"
	}
	action.RawQuery = v.Encode()
	return action.String(), ""
}

// listDriveFolder lists a shared folder through its embeddable view, which
// needs no API key: files lazily, subfolders recursively.
func (c *Client) listDriveFolder(ctx context.Context, id, rk, prefix string, depth int) ([]*File, error) {
	if depth > 4 {
		return nil, nil
	}
	v := url.Values{"id": {id}}
	if rk != "" {
		v.Set("resourcekey", rk)
	}
	resp, err := c.get(ctx, "https://drive.google.com/embeddedfolderview?"+v.Encode())
	if err != nil {
		return nil, fmt.Errorf("Google Drive folder %s: %w", id, err)
	}
	doc, err := html.Parse(io.LimitReader(resp.Body, 20<<20))
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	var files []*File
	var errs []error
	walk(doc, func(n *html.Node) bool {
		if n.Type != html.ElementNode || n.Data != "div" || !hasClass(n, "flip-entry") {
			return true
		}
		var href, name string
		walk(n, func(m *html.Node) bool {
			if m.Type == html.ElementNode {
				if m.Data == "a" && href == "" {
					href = attr(m, "href")
				}
				if hasClass(m, "flip-entry-title") {
					name = text(m)
				}
			}
			return true
		})
		if href == "" || name == "" {
			return false
		}
		eu, err := url.Parse(href)
		if err != nil {
			return false
		}
		erk := eu.Query().Get("resourcekey")
		if m := driveFolderID.FindStringSubmatch(eu.Path); m != nil {
			sub, err := c.listDriveFolder(ctx, m[1], erk, prefix+SafeName(name)+"/", depth+1)
			if err != nil {
				errs = append(errs, err)
			}
			files = append(files, sub...)
		} else if m := driveFileID.FindStringSubmatch(eu.Path); m != nil {
			fileID := m[1]
			files = append(files, &File{
				Path: prefix + name,
				Size: -1,
				fetch: func(ctx context.Context, dir string) (string, error) {
					return c.downloadDrive(ctx, fileID, erk, dir)
				},
			})
		}
		return false
	})
	if len(files) == 0 && len(errs) > 0 {
		return nil, errs[0]
	}
	return files, nil
}

// walk visits n and its descendants; visit returns false to skip children.
func walk(n *html.Node, visit func(*html.Node) bool) {
	if !visit(n) {
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walk(c, visit)
	}
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hasClass(n *html.Node, class string) bool {
	for _, c := range strings.Fields(attr(n, "class")) {
		if c == class {
			return true
		}
	}
	return false
}

func text(n *html.Node) string {
	var b strings.Builder
	walk(n, func(m *html.Node) bool {
		if m.Type == html.TextNode {
			b.WriteString(m.Data)
		}
		return true
	})
	return strings.Join(strings.Fields(b.String()), " ")
}
