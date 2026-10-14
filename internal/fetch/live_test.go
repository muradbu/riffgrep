package fetch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muradbu/riffgrep/internal/pick"
)

// Real downloads; run with RIFFGREP_LINKS set to space-separated download
// links (from Ignition, so not kept in the repo).
func TestLive(t *testing.T) {
	links := strings.Fields(os.Getenv("RIFFGREP_LINKS"))
	if len(links) == 0 {
		t.Skip("set RIFFGREP_LINKS to download links to test the hosts with")
	}
	c, err := NewClient()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, link := range links {
		t.Run(redact(link), func(t *testing.T) {
			dest := t.TempDir()
			got, err := c.Fetch(context.Background(), link, pick.Prefs{Platform: pick.PC, DD: true}, dest)
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range got {
				st, _ := os.Stat(p)
				t.Logf("%s (%d bytes)", filepath.Base(p), st.Size())
				if !strings.HasSuffix(p, "_p.psarc") && !strings.HasSuffix(p, ".psarc") {
					t.Errorf("unexpected file %s", p)
				}
			}
		})
	}
}
