package ignition

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Against the real site with the saved login; run with RIFFGREP_LIVE=1.
func TestLive(t *testing.T) {
	if os.Getenv("RIFFGREP_LIVE") == "" {
		t.Skip("set RIFFGREP_LIVE=1 to query Ignition")
	}
	dir, _ := os.UserConfigDir()
	b, err := os.ReadFile(filepath.Join(dir, "riffgrep", "cookie"))
	if err != nil {
		t.Fatal(err)
	}
	ck, err := ParseCookie(string(b))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := New(ck)
	ctx := context.Background()
	user, err := c.User(ctx)
	t.Logf("user %q err %v", user, err)
	rows, err := c.Search(ctx, Query{Search: "Master of Puppets", Type: Album})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		t.Logf("%d | %s | %s | %s | %s | %s | v%s | %s | %d | %s | %s | owned=%v", r.ID, r.Artist, r.Title, r.Album, r.Year, r.Tuning, r.Version, r.Parts, r.Downloads, r.Updated.Format("2006-01-02"), r.Host, r.Owned)
	}
	rows, err = c.Search(ctx, Query{Search: "Metallica", Type: Artist})
	t.Logf("Metallica: %d rows, err %v", len(rows), err)
	rows, err = c.Search(ctx, Query{Lens: Collection, Search: "Infinite Source", Type: Title})
	if err != nil || len(rows) == 0 {
		t.Fatalf("collection: %v %v", rows, err)
	}
	link, err := c.Link(ctx, rows[0], Mac)
	t.Logf("link %s err %v", link, err)
	if err != nil || !strings.HasPrefix(link, "https://") {
		t.Fail()
	}
	names, err := c.Artists(ctx, "metalli")
	t.Logf("artists %v %v", names, err)
}
