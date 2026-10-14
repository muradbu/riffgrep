// Package cmd is riffgrep's command line.
package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/spf13/cobra"

	"github.com/muradbu/riffgrep/internal/ignition"
	"github.com/muradbu/riffgrep/internal/library"
	"github.com/muradbu/riffgrep/internal/pick"
)

var version = "dev"

// Download options shared by the commands that download.
type dlFlags struct {
	dest        string
	platform    string
	noDD        bool
	jobs        int
	dryRun      bool
	owned       bool
	allVersions bool
}

func (f *dlFlags) register(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVarP(&f.dest, "dest", "d", "", "folder to download into (default: Rocksmith's dlc folder, one subfolder per artist)")
	fl.StringVarP(&f.platform, "platform", "p", "", `"pc" or "mac" (default: this computer's)`)
	fl.BoolVar(&f.noDD, "no-dd", false, "prefer files without dynamic difficulty")
	fl.IntVarP(&f.jobs, "jobs", "j", 0, "downloads at once (default 4)")
	fl.BoolVarP(&f.dryRun, "dry-run", "n", false, "only list what would be downloaded")
	fl.BoolVar(&f.owned, "owned", false, "also download songs already in your Ignition collection")
	fl.BoolVar(&f.allVersions, "all-versions", false, "download every chart of a song, not just the most downloaded one")
}

// settings merges the config file with the flags.
func (f *dlFlags) settings() (library.Config, pick.Prefs, error) {
	cfg, err := library.LoadConfig()
	if err != nil {
		return cfg, pick.Prefs{}, err
	}
	if f.platform != "" {
		cfg.Platform = f.platform
	}
	if f.noDD {
		cfg.DD = false
	}
	if f.jobs > 0 {
		cfg.Jobs = f.jobs
	}
	if cfg.Jobs < 1 {
		cfg.Jobs = 1
	}
	prefs := cfg.Prefs()
	if prefs.Platform != pick.PC && prefs.Platform != pick.Mac {
		return cfg, prefs, fmt.Errorf("platform %q: use pc or mac", cfg.Platform)
	}
	return cfg, prefs, nil
}

func client() (*ignition.Client, error) {
	ck, err := library.Cookie()
	if err != nil {
		return nil, err
	}
	if ck == "" {
		return nil, ignition.ErrLogin
	}
	ck, err = ignition.ParseCookie(ck)
	if err != nil {
		return nil, fmt.Errorf("saved login: %w; run `riffgrep login` again", err)
	}
	return ignition.New(ck)
}

func Execute() {
	root := &cobra.Command{
		Use:   "riffgrep",
		Short: "Search CustomsForge Ignition and download Rocksmith 2014 CDLC",
		Long: `riffgrep searches CustomsForge's Ignition4 catalog and downloads CDLC
straight into Rocksmith 2014's dlc folder, a whole artist or album at once.
It follows each chart's download link (Google Drive, Dropbox, MediaFire, ...),
unpacks archives and keeps the file for your platform, with dynamic
difficulty when there's a choice.

Downloads count as downloads on Ignition: they're added to your collection,
so its "Updates" list (riffgrep updates) works for them.`,
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(loginCmd(), searchCmd(), artistCmd(), albumCmd(), getCmd(), updatesCmd(), configCmd())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := root.ExecuteContext(ctx); err != nil {
		msg := err.Error()
		if errors.Is(err, ignition.ErrLogin) {
			msg = ignition.ErrLogin.Error()
		}
		fmt.Fprintln(os.Stderr, "riffgrep:", msg)
		os.Exit(1)
	}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, strings.TrimSuffix(word, "s"))
}
