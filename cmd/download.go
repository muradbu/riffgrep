package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/muradbu/riffgrep/internal/fetch"
	"github.com/muradbu/riffgrep/internal/ignition"
	"github.com/muradbu/riffgrep/internal/library"
)

func filter(rows []ignition.CDLC, keep func(ignition.CDLC) bool) []ignition.CDLC {
	var out []ignition.CDLC
	for _, r := range rows {
		if keep(r) {
			out = append(out, r)
		}
	}
	return out
}

var nonAlnum = regexp.MustCompile(`[^\p{L}\p{N}]+`)

func norm(s string) string {
	return strings.TrimSpace(nonAlnum.ReplaceAllString(strings.ToLower(s), " "))
}

func sameName(a, b string) bool { return norm(a) == norm(b) }

var (
	// "(Remastered)", "- 2011 Remaster": the same song as far as picking a
	// chart goes.
	remaster  = regexp.MustCompile(`(?i)[\s(\[-]+(\d{4}\s+)?remaster(ed)?(\s+\d{4})?(\s+version)?[)\]]?\s*$`)
	fullAlbum = regexp.MustCompile(`(?i)\bfull[\s-]+album\b`)
)

func songKey(r ignition.CDLC) string {
	return norm(r.Artist) + "\x00" + norm(remaster.ReplaceAllString(r.Title, ""))
}

// choose picks what a bulk download gets: one chart per song (the most
// downloaded), skipping whole-album charts and songs you already own.
func choose(rows []ignition.CDLC, owned, allVersions bool) []ignition.CDLC {
	state, _ := library.LoadState()
	groups := map[string][]ignition.CDLC{}
	var order []string
	for _, r := range rows {
		if fullAlbum.MatchString(r.Title) {
			fmt.Printf("skipping %s – %s (a whole-album chart)\n", r.Artist, r.Title)
			continue
		}
		k := songKey(r)
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], r)
	}
	var out []ignition.CDLC
	skipped := 0
	for _, k := range order {
		g := groups[k]
		if !owned && ownsAny(g, state) {
			skipped++
			continue
		}
		if allVersions {
			out = append(out, g...)
			continue
		}
		sort.SliceStable(g, func(i, j int) bool { return g[i].Downloads > g[j].Downloads })
		out = append(out, g[0])
	}
	if skipped > 0 {
		fmt.Printf("skipping %s already in your collection (--owned to include)\n", plural(skipped, "song"))
	}
	return out
}

// ownsAny: one of a song's charts is in the collection, and not because a
// riffgrep download of it failed after Ignition had counted it.
func ownsAny(g []ignition.CDLC, state *library.State) bool {
	for _, r := range g {
		if _, failed := state.Failed[r.ID]; r.Owned && !failed {
			return true
		}
	}
	return false
}

func noArtist(cmd *cobra.Command, c *ignition.Client, name string) error {
	names := suggest(cmd.Context(), c, name)
	if len(names) == 0 {
		return fmt.Errorf("no artist %q on Ignition", name)
	}
	return fmt.Errorf("no artist %q; did you mean: %s", name, strings.Join(names, ", "))
}

// suggest finds artist names close to a misspelled one. Ignition's artist
// search matches substrings, 10 at most, so ask for every 4-letter piece of
// the name and rank what comes back by edit distance.
func suggest(ctx context.Context, c *ignition.Client, name string) []string {
	r := []rune(strings.ToLower(name))
	pieces := map[string]bool{}
	for i := 0; i+4 <= len(r) || i == 0; i++ {
		pieces[string(r[i:min(i+4, len(r))])] = true
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	found := map[string]bool{}
	for p := range pieces {
		wg.Add(1)
		go func() {
			defer wg.Done()
			names, _ := c.Artists(ctx, p)
			mu.Lock()
			for _, n := range names {
				found[n] = true
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	type cand struct {
		name string
		d    int
	}
	var cands []cand
	for n := range found {
		if d := distance(norm(n), norm(name)); d <= max(2, len(r)/3) {
			cands = append(cands, cand{n, d})
		}
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].d != cands[j].d {
			return cands[i].d < cands[j].d
		}
		return cands[i].name < cands[j].name
	})
	var out []string
	for _, c := range cands[:min(5, len(cands))] {
		out = append(out, c.name)
	}
	return out
}

func distance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}

type result struct {
	row   ignition.CDLC
	files []string
	link  string
	err   error
}

func download(ctx context.Context, c *ignition.Client, f *dlFlags, rows []ignition.CDLC) error {
	cfg, prefs, err := f.settings()
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Println("Nothing to download.")
		return nil
	}
	if f.dryRun {
		printRows(rows)
		return nil
	}
	base := f.dest
	if base == "" {
		dlc, err := cfg.FindDlcDir()
		if err != nil {
			return err
		}
		base = filepath.Join(dlc, cfg.Subdir)
	}
	state, err := library.LoadState()
	if err != nil {
		return err
	}
	fc, err := fetch.NewClient()
	if err != nil {
		return err
	}
	defer fc.Close()

	dd := "DD"
	if !prefs.DD {
		dd = "no DD"
	}
	fmt.Printf("Downloading %s (%s, %s) into %s\n", plural(len(rows), "chart"), prefs.Platform, dd, base)
	start := time.Now()

	// A human check from Ignition stops the batch: every further link would
	// hit it too.
	ctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)

	jobs := make(chan ignition.CDLC)
	results := make(chan result)
	var wg sync.WaitGroup
	for range min(cfg.Jobs, len(rows)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := range jobs {
				res := result{row: r}
				res.link, res.err = c.Link(ctx, r, string(prefs.Platform))
				if errors.Is(res.err, ignition.ErrChallenge) {
					stop(res.err)
				}
				if res.err == nil {
					dest := filepath.Join(base, fetch.SafeName(r.Artist))
					res.files, res.err = fc.Fetch(ctx, res.link, prefs, dest)
				}
				results <- res
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, r := range rows {
			select {
			case jobs <- r:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { wg.Wait(); close(results) }()

	var failed []result
	ok := map[int]bool{}
	done := 0
	for res := range results {
		r := res.row
		if res.err != nil && errors.Is(context.Cause(ctx), ignition.ErrChallenge) {
			continue // listed below for a later `riffgrep get`
		}
		done++
		name := fmt.Sprintf("%s – %s", r.Artist, r.Title)
		prefix := fmt.Sprintf("[%d/%d]", done, len(rows))
		if res.err != nil {
			fmt.Printf("%s ✗ %s: %v\n", prefix, name, res.err)
			failed = append(failed, res)
			state.Failed[r.ID] = res.err.Error()
			continue
		}
		var names []string
		for _, p := range res.files {
			names = append(names, filepath.Base(p))
		}
		fmt.Printf("%s ✓ %s → %s\n", prefix, name, strings.Join(names, ", "))
		ok[r.ID] = true
		if old, ok := state.CDLC[r.ID]; ok {
			removeReplaced(old.Files, res.files)
		}
		delete(state.Failed, r.ID)
		state.CDLC[r.ID] = library.Entry{Artist: r.Artist, Title: r.Title, Version: r.Version, Files: res.files, At: time.Now()}
	}
	if err := state.Save(); err != nil {
		fmt.Fprintln(os.Stderr, "riffgrep: saving library.json:", err)
	}
	if errors.Is(context.Cause(ctx), ignition.ErrChallenge) {
		var left []string
		for _, r := range rows {
			if !ok[r.ID] {
				left = append(left, strconv.Itoa(r.ID))
			}
		}
		fmt.Printf("\n%s downloaded. Ignition wants a quick human check before more downloads. Open\n  %s\nin your browser, pass it, then get the rest with\n  riffgrep get %s\n",
			plural(len(ok), "chart"), ignition.ChallengeURL, strings.Join(left, " "))
		return ignition.ErrChallenge
	}
	fmt.Printf("\n%s downloaded in %s", plural(len(rows)-len(failed), "chart"), time.Since(start).Round(time.Second))
	if len(failed) == 0 {
		fmt.Println(".")
		return ctx.Err()
	}
	fmt.Printf(", %d failed. Get these in a browser:\n", len(failed))
	for _, res := range failed {
		where := res.link
		if where == "" || !errors.Is(res.err, fetch.ErrUnsupported) {
			where = res.row.URL()
		}
		fmt.Printf("  %s – %s: %s\n", res.row.Artist, res.row.Title, where)
	}
	return fmt.Errorf("%s failed", plural(len(failed), "download"))
}

// removeReplaced deletes an update's old files that the new download
// didn't overwrite.
func removeReplaced(old, kept []string) {
	keep := map[string]bool{}
	for _, p := range kept {
		keep[p] = true
	}
	for _, p := range old {
		if !keep[p] {
			if err := os.Remove(p); err == nil {
				fmt.Printf("      removed the old %s\n", filepath.Base(p))
			}
		}
	}
}
