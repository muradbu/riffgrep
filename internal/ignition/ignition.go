// Package ignition talks to CustomsForge's Ignition4 CDLC catalog
// (https://ignition4.customsforge.com), a Laravel/Livewire site without a
// public API: search results are its HTML table rows.
//
// Login is the site's own: Laravel's long-lived remember cookie
// (remember_web_<hash>) is enough for it to start a session, so that's all
// riffgrep stores. Each run gets a fresh session and CSRF token from the first
// page it loads.
package ignition

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/sync/errgroup"
)

const Base = "https://ignition4.customsforge.com"

const userAgent = "riffgrep (+https://github.com/muradbu/riffgrep)"

// ErrChallenge means Ignition wants a human check (a Cloudflare Turnstile
// "Quick check") before handing out more download links. riffgrep doesn't
// try to get past it: the user passes it in a browser.
var ErrChallenge = errors.New("Ignition asks for a human check before more downloads")

// ChallengeURL is where that check is passed.
const ChallengeURL = Base + "/firewall/challenge"

// ErrLogin means Ignition sent us to its login page: the cookie is missing,
// expired, or was ended by logging out.
var ErrLogin = errors.New("not logged in to Ignition (run `riffgrep login`)")

type Client struct {
	http *http.Client

	mu   sync.Mutex
	csrf string
}

// CookieName is the prefix of Laravel's remember-me cookie on Ignition.
const CookieName = "remember_web_"

// ParseCookie accepts what a user copies from the browser: the remember
// cookie as name=value, its bare value, or a whole Cookie header. It returns
// the remember cookie as name=value.
func ParseCookie(s string) (string, error) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "Cookie:"))
	for _, part := range strings.Split(s, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, CookieName) && strings.Contains(part, "=") {
			return part, nil
		}
	}
	// A bare value: the cookie name is fixed for the site (a hash of
	// Laravel's guard class), so it can be supplied.
	if s != "" && !strings.ContainsAny(s, "=; ") {
		return CookieName + "59ba36addc2b2f9401580f014c7f58ea4e30989d=" + s, nil
	}
	return "", fmt.Errorf("no %s… cookie in what was given", CookieName)
}

func New(cookie string) (*Client, error) {
	name, value, ok := strings.Cut(cookie, "=")
	if !ok {
		return nil, fmt.Errorf("bad cookie %q", name)
	}
	jar, _ := cookiejar.New(nil)
	u, _ := url.Parse(Base)
	jar.SetCookies(u, []*http.Cookie{{Name: name, Value: value, Domain: ".customsforge.com", Path: "/", Secure: true}})
	return &Client{http: &http.Client{Jar: jar, Timeout: 60 * time.Second}}, nil
}

func (c *Client) do(req *http.Request) (*http.Response, error) {
	req.Header.Set("User-Agent", userAgent)
	for attempt := 0; ; attempt++ {
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}
		if (resp.StatusCode == 429 || resp.StatusCode >= 502) && attempt < 4 {
			resp.Body.Close()
			wait := time.Duration(1<<attempt) * time.Second
			if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil {
				wait = time.Duration(s) * time.Second
			}
			select {
			case <-time.After(wait):
			case <-req.Context().Done():
				return nil, req.Context().Err()
			}
			if req.GetBody != nil {
				req.Body, _ = req.GetBody()
			}
			continue
		}
		return resp, nil
	}
}

// page loads an Ignition page as parsed HTML and keeps its CSRF token.
func (c *Client) page(ctx context.Context, path string) (*html.Node, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", Base+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if loggedOut(resp.Request.URL) {
		return nil, ErrLogin
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s: HTTP %s", path, resp.Status)
	}
	doc, err := html.Parse(resp.Body)
	if err != nil {
		return nil, err
	}
	walk(doc, func(n *html.Node) bool {
		if n.Type == html.ElementNode && n.Data == "meta" && attr(n, "name") == "csrf-token" {
			c.mu.Lock()
			c.csrf = attr(n, "content")
			c.mu.Unlock()
			return false
		}
		return n.Data != "body"
	})
	return doc, nil
}

func loggedOut(u *url.URL) bool {
	return u.Host != "ignition4.customsforge.com" || u.Path == "/login" || strings.HasPrefix(u.Path, "/auth/")
}

// User returns the logged-in user's name, to check a cookie.
func (c *Client) User(ctx context.Context) (string, error) {
	doc, err := c.page(ctx, "/user/settings")
	if err != nil {
		return "", err
	}
	var name string
	walk(doc, func(n *html.Node) bool {
		if name != "" {
			return false
		}
		if n.Type == html.ElementNode && n.Data == "a" {
			if m := profileLink.FindStringSubmatch(attr(n, "href")); m != nil {
				name, _ = url.PathUnescape(m[1])
			}
		}
		return true
	})
	if name == "" {
		return "", errors.New("logged in, but couldn't find the user name")
	}
	return name, nil
}

var profileLink = regexp.MustCompile(`^https://ignition4\.customsforge\.com/profile/([^/?#]+)$`)

// CDLC is one row of the catalog.
type CDLC struct {
	ID        int
	Artist    string
	Title     string
	Album     string
	Year      string
	Tuning    string
	Parts     string // L R B V (lead, rhythm, bass, vocals)
	Version   string
	Creator   string
	Duration  string
	Downloads int
	Added     time.Time
	Updated   time.Time
	Host      string // where the file is, e.g. drive.google.com
	Owned     bool   // in your Ignition collection
	toggle    string // signed URL that adds it to the collection and hands out the download link
}

// Platform names as Ignition's download links use them.
const (
	PC  = "pc"
	Mac = "mac"
)

func (c CDLC) URL() string { return fmt.Sprintf("%s/cdlc/%d", Base, c.ID) }

// SearchType narrows a search to one field.
type SearchType string

const (
	Any     SearchType = "type"
	Artist  SearchType = "artist"
	Album   SearchType = "album"
	Title   SearchType = "title"
	Creator SearchType = "creator"
)

// Lens is a view of the catalog: everything, or a personal subset.
type Lens string

const (
	All        Lens = "/"
	Collection Lens = "/search/collection"
	Updated    Lens = "/search/updated" // in your collection, updated since you got it
)

type Query struct {
	Lens   Lens
	Search string
	Type   SearchType
	// MaxPages stops early on huge result sets (25 rows a page); 0: all.
	MaxPages int
}

func (q Query) path(page int) string {
	v := url.Values{}
	if q.Search != "" {
		v.Set("search", q.Search)
		t := q.Type
		if t == "" {
			t = Any
		}
		v.Set("search_type", string(t))
	}
	if page > 1 {
		v.Set("page", strconv.Itoa(page))
	}
	lens := q.Lens
	if lens == "" {
		lens = All
	}
	if len(v) == 0 {
		return string(lens)
	}
	return string(lens) + "?" + v.Encode()
}

// Search returns all rows of a query, fetching its pages in parallel.
func (c *Client) Search(ctx context.Context, q Query) ([]CDLC, error) {
	doc, err := c.page(ctx, q.path(1))
	if err != nil {
		return nil, err
	}
	first, pages := parseRows(doc)
	if q.MaxPages > 0 {
		pages = min(pages, q.MaxPages)
	}
	results := make([][]CDLC, pages+1)
	results[1] = first
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(4)
	for p := 2; p <= pages; p++ {
		g.Go(func() error {
			doc, err := c.page(gctx, q.path(p))
			if err != nil {
				return err
			}
			results[p], _ = parseRows(doc)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	var all []CDLC
	seen := map[int]bool{}
	for _, rows := range results {
		for _, r := range rows {
			if !seen[r.ID] {
				seen[r.ID] = true
				all = append(all, r)
			}
		}
	}
	return all, nil
}

var (
	gotoPage = regexp.MustCompile(`gotoPage\((\d+),`)
	hostedOn = regexp.MustCompile(`Hosted on (\S+)`)
)

func parseRows(doc *html.Node) (rows []CDLC, pages int) {
	pages = 1
	walk(doc, func(n *html.Node) bool {
		if n.Type != html.ElementNode {
			return true
		}
		if m := gotoPage.FindStringSubmatch(attr(n, "wire:click")); m != nil {
			p, _ := strconv.Atoi(m[1])
			pages = max(pages, p)
		}
		if n.Data == "tr" && attr(n, "data-cdlc-id") != "" && strings.HasPrefix(attr(n, "wire:key"), "cdlc-") {
			if r, ok := parseRow(n); ok {
				rows = append(rows, r)
			}
			return false
		}
		return true
	})
	return rows, pages
}

func parseRow(tr *html.Node) (CDLC, bool) {
	id, err := strconv.Atoi(attr(tr, "data-cdlc-id"))
	if err != nil {
		return CDLC{}, false
	}
	r := CDLC{ID: id}
	for td := tr.FirstChild; td != nil; td = td.NextSibling {
		if td.Type != html.ElementNode || td.Data != "td" {
			continue
		}
		classes := strings.Fields(attr(td, "class"))
		if len(classes) == 0 {
			continue
		}
		switch classes[0] {
		case "add":
			walk(td, func(n *html.Node) bool {
				if n.Type == html.ElementNode && n.Data == "a" && hasClass(n, "add_collection") && r.toggle == "" {
					r.toggle = attr(n, "href")
					title := attr(n, "title")
					r.Owned = strings.HasPrefix(title, "Redownload")
					if m := hostedOn.FindStringSubmatch(title); m != nil {
						r.Host = m[1]
					}
				}
				return true
			})
		case "artist":
			r.Artist = cellText(td)
		case "title":
			r.Title = cellText(td)
		case "album":
			r.Album = cellText(td)
		case "year":
			r.Year = cellText(td)
		case "tuning":
			r.Tuning = cellText(td)
		case "parts":
			r.Parts = cellText(td)
		case "version":
			r.Version = cellText(td)
		case "member":
			r.Creator = cellText(td)
		case "duration":
			r.Duration = cellText(td)
		case "downloads":
			r.Downloads, _ = strconv.Atoi(strings.ReplaceAll(cellText(td), ",", ""))
		case "added":
			r.Added, _ = time.Parse("01/02/2006", cellText(td))
		case "updated":
			r.Updated, _ = time.Parse("01/02/2006", cellText(td))
		}
	}
	return r, r.toggle != ""
}

// cellText is a cell's visible text, without hidden menus and scripts.
func cellText(td *html.Node) string {
	var b strings.Builder
	walk(td, func(n *html.Node) bool {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "template", "button":
				return false
			}
			if attr(n, "x-show") != "" || attr(n, "role") == "menu" || hasClass(n, "hidden") || hasClass(n, "sr-only") {
				return false
			}
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			b.WriteByte(' ')
		}
		return true
	})
	return strings.Join(strings.Fields(b.String()), " ")
}

// Link returns where a CDLC's file for a platform is hosted (a Drive,
// Dropbox, ... URL). Like the site's download button, this adds it to your
// collection.
//
// Search rows only have the link for your Ignition default platform; the
// CDLC's own page has one per platform it supports. Without a link for the
// platform asked for, the PC one is returned, since packs often hold both.
func (c *Client) Link(ctx context.Context, cd CDLC, platform string) (string, error) {
	page, err := c.get(ctx, cd.ID)
	if err != nil {
		return "", err
	}
	toggle := page.toggles[platform]
	if toggle == "" {
		toggle = page.toggles[PC]
	}
	if toggle == "" {
		toggle = page.toggles[""]
	}
	dl, err := c.downloadURL(ctx, toggle)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", dl, nil)
	if err != nil {
		return "", err
	}
	noFollow := *c.http
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req.Header.Set("User-Agent", userAgent)
	resp, err := noFollow.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if resp.StatusCode/100 != 3 || loc == "" {
		return "", fmt.Errorf("CDLC %d: no download link (HTTP %s)", cd.ID, resp.Status)
	}
	if u, err := url.Parse(loc); err == nil && u.Host == "ignition4.customsforge.com" {
		switch u.Path {
		case "/external-link":
			loc = u.Query().Get("url")
		case "/firewall/challenge":
			return "", ErrChallenge
		case "/login":
			return "", ErrLogin
		}
	}
	return loc, nil
}

func (c *Client) downloadURL(ctx context.Context, toggle string) (string, error) {
	if toggle == "" {
		return "", errors.New("no download button")
	}
	c.mu.Lock()
	csrf := c.csrf
	c.mu.Unlock()
	req, err := http.NewRequestWithContext(ctx, "PATCH", toggle, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("X-CSRF-TOKEN", csrf)
	resp, err := c.do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var r struct {
		Success      bool   `json:"success"`
		DownloadLink string `json:"downloadLink"`
		ToastText    string `json:"toastText"`
	}
	if err := json.Unmarshal(body, &r); err != nil || r.DownloadLink == "" {
		msg := r.ToastText
		if msg == "" {
			msg = "HTTP " + resp.Status
		}
		return "", fmt.Errorf("Ignition: %s", msg)
	}
	return r.DownloadLink, nil
}

type cdlcPage struct {
	CDLC
	toggles map[string]string // platform ("pc", "mac", or "" when unsaid) to signed toggle URL
}

// Get reads a CDLC's own page. Only what downloading needs is filled in:
// artist, title, creator, host and whether you own it.
func (c *Client) Get(ctx context.Context, id int) (CDLC, error) {
	p, err := c.get(ctx, id)
	return p.CDLC, err
}

func (c *Client) get(ctx context.Context, id int) (cdlcPage, error) {
	doc, err := c.page(ctx, fmt.Sprintf("/cdlc/%d", id))
	if err != nil {
		return cdlcPage{}, err
	}
	r := cdlcPage{CDLC: CDLC{ID: id}, toggles: map[string]string{}}
	want := fmt.Sprintf("/user/collectedcdlcs/toggle/%d?", id)
	walk(doc, func(n *html.Node) bool {
		if n.Type != html.ElementNode {
			return true
		}
		switch n.Data {
		case "title":
			// "CustomsForge | Ignition4 | <artist> - <title> by <creator>"
			t := cellText(n)
			if i := strings.LastIndex(t, " | "); i >= 0 {
				t = t[i+3:]
			}
			if i := strings.LastIndex(t, " by "); i >= 0 {
				t, r.Creator = t[:i], t[i+4:]
			}
			r.Artist, r.Title, _ = strings.Cut(t, " - ")
		case "a":
			h := attr(n, "href")
			if !strings.Contains(h, want) || strings.Contains(h, "remove=1") {
				return true
			}
			platform := ""
			if u, err := url.Parse(h); err == nil {
				platform = u.Query().Get("platform")
			}
			if _, ok := r.toggles[platform]; !ok {
				r.toggles[platform] = h
			}
			title := attr(n, "title")
			if strings.HasPrefix(title, "Redownload") {
				r.Owned = true
			}
			if m := hostedOn.FindStringSubmatch(title); m != nil && r.Host == "" {
				r.Host = m[1]
			}
		}
		return true
	})
	if len(r.toggles) == 0 {
		return r, fmt.Errorf("CDLC %d: no download button on its page (removed?)", id)
	}
	return r, nil
}

// Artists suggests artist names for a search, like the site's search box.
func (c *Client) Artists(ctx context.Context, q string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", Base+"/api/artists/search?q="+url.QueryEscape(q), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if loggedOut(resp.Request.URL) {
		return nil, ErrLogin
	}
	var r struct {
		Artists []struct {
			Name string `json:"name"`
		} `json:"artists"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	var names []string
	for _, a := range r.Artists {
		names = append(names, a.Name)
	}
	return names, nil
}

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
