package cmd

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/muradbu/riffgrep/internal/ignition"
	"github.com/muradbu/riffgrep/internal/library"
)

func loginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login [cookie]",
		Short: "Save your Ignition login",
		Long: `Save your Ignition login: the browser's remember_web_… cookie, which
keeps you logged in for about a year. To get it:

  1. Log in at https://ignition4.customsforge.com
  2. Open the browser's developer tools (F12, or ⌥⌘I on a Mac) and go to
     Application (Chrome, Edge) or Storage (Firefox, Safari) > Cookies >
     https://ignition4.customsforge.com
  3. Copy the Value of the cookie whose name starts with remember_web_

Paste it when asked, or pass it as the argument. A whole Cookie header
(from the Network tab) works too. Logging out on the site ends it.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			raw := ""
			if len(args) == 1 {
				raw = args[0]
			} else {
				fmt.Fprint(os.Stderr, "remember_web_… cookie value: ")
				line, err := bufio.NewReader(os.Stdin).ReadString('\n')
				if err != nil && line == "" {
					return err
				}
				raw = line
			}
			ck, err := ignition.ParseCookie(raw)
			if err != nil {
				return err
			}
			c, err := ignition.New(ck)
			if err != nil {
				return err
			}
			user, err := c.User(cmd.Context())
			if err != nil {
				return fmt.Errorf("that cookie doesn't log in: %w", err)
			}
			if err := library.SaveCookie(ck); err != nil {
				return err
			}
			fmt.Printf("Logged in as %s.\n", user)
			return nil
		},
	}
}

func searchCmd() *cobra.Command {
	var typ string
	var owned, updated bool
	var pages int
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search the catalog",
		Long: `Search the catalog, newest updates first. IDs in the first column go to
riffgrep get. ✓ marks songs in your collection.`,
		Example: `  riffgrep search master of puppets
  riffgrep search -t artist "Arctic Monkeys"
  riffgrep search -t creator Djpavs
  riffgrep search --updated`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			q := ignition.Query{Search: strings.Join(args, " "), Type: ignition.SearchType(typ), MaxPages: pages}
			switch {
			case updated:
				q.Lens = ignition.Updated
			case owned:
				q.Lens = ignition.Collection
			}
			if q.Search == "" && q.Lens == "" {
				return fmt.Errorf("search for what?")
			}
			c, err := client()
			if err != nil {
				return err
			}
			rows, err := c.Search(cmd.Context(), q)
			if err != nil {
				return err
			}
			printRows(rows)
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVarP(&typ, "type", "t", "type", "search one field: artist, album, title or creator")
	fl.BoolVar(&owned, "owned", false, "search your collection only")
	fl.BoolVar(&updated, "updated", false, "songs in your collection with an update")
	fl.IntVar(&pages, "pages", 20, "stop after this many pages of 25 (0: all)")
	return cmd
}

func printRows(rows []ignition.CDLC) {
	if len(rows) == 0 {
		fmt.Println("Nothing found.")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\t\tARTIST\tTITLE\tALBUM\tTUNING\tPARTS\tVER\tDLS\tHOST")
	for _, r := range rows {
		mark := ""
		if r.Owned {
			mark = "✓"
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\n", r.ID, mark,
			truncate(r.Artist, 24), truncate(r.Title, 40), truncate(r.Album, 28),
			truncate(r.Tuning, 14), r.Parts, r.Version, r.Downloads, strings.TrimPrefix(r.Host, "www."))
	}
	w.Flush()
	fmt.Printf("%s\n", plural(len(rows), "result"))
}

func artistCmd() *cobra.Command {
	var f dlFlags
	cmd := &cobra.Command{
		Use:   "artist <name>",
		Short: "Download every song by an artist",
		Long: `Download every song charted for an artist (exact name; riffgrep suggests
names when there's no match). Per song only the most downloaded chart is
taken, and songs in your collection are skipped.`,
		Example: `  riffgrep artist Metallica
  riffgrep artist -n "Royal Blood"     # just list them`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.Join(args, " ")
			c, err := client()
			if err != nil {
				return err
			}
			rows, err := c.Search(cmd.Context(), ignition.Query{Search: name, Type: ignition.Artist})
			if err != nil {
				return err
			}
			rows = filter(rows, func(r ignition.CDLC) bool { return sameName(r.Artist, name) })
			if len(rows) == 0 {
				return noArtist(cmd, c, name)
			}
			return download(cmd.Context(), c, &f, choose(rows, f.owned, f.allVersions))
		},
	}
	f.register(cmd)
	return cmd
}

func albumCmd() *cobra.Command {
	var f dlFlags
	var artist string
	cmd := &cobra.Command{
		Use:   "album <album>",
		Short: "Download an album",
		Long: `Download every song charted from an album. When several artists have an
album by that name, pick one with --artist.`,
		Example: `  riffgrep album "Master of Puppets" -a Metallica
  riffgrep album -n Ten`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.Join(args, " ")
			c, err := client()
			if err != nil {
				return err
			}
			rows, err := c.Search(cmd.Context(), ignition.Query{Search: name, Type: ignition.Album})
			if err != nil {
				return err
			}
			rows = filter(rows, func(r ignition.CDLC) bool {
				return sameName(r.Album, name) && (artist == "" || sameName(r.Artist, artist))
			})
			if len(rows) == 0 {
				return fmt.Errorf("no album %q%s; try `riffgrep search -t album %q`", name, byArtist(artist), name)
			}
			artists := map[string]bool{}
			for _, r := range rows {
				artists[r.Artist] = true
			}
			if len(artists) > 1 {
				var names []string
				for a := range artists {
					names = append(names, a)
				}
				sort.Strings(names)
				return fmt.Errorf("%q is an album by %s; pick one with --artist", name, strings.Join(names, ", "))
			}
			return download(cmd.Context(), c, &f, choose(rows, f.owned, f.allVersions))
		},
	}
	cmd.Flags().StringVarP(&artist, "artist", "a", "", "the album's artist")
	f.register(cmd)
	return cmd
}

func byArtist(a string) string {
	if a == "" {
		return ""
	}
	return " by " + a
}

func getCmd() *cobra.Command {
	var f dlFlags
	cmd := &cobra.Command{
		Use:   "get <id or url>...",
		Short: "Download charts by ID",
		Long:  `Download charts by their Ignition ID (from riffgrep search) or page URL.`,
		Example: `  riffgrep get 20769 50913
  riffgrep get https://ignition4.customsforge.com/cdlc/20769`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			var rows []ignition.CDLC
			for _, a := range args {
				id, err := strconv.Atoi(strings.TrimRight(a[strings.LastIndex(a, "/")+1:], "#"))
				if err != nil {
					return fmt.Errorf("%q: not an ID or Ignition URL", a)
				}
				r, err := c.Get(cmd.Context(), id)
				if err != nil {
					return err
				}
				rows = append(rows, r)
			}
			f.owned, f.allVersions = true, true
			return download(cmd.Context(), c, &f, rows)
		},
	}
	f.register(cmd)
	return cmd
}

func updatesCmd() *cobra.Command {
	var f dlFlags
	cmd := &cobra.Command{
		Use:   "updates",
		Short: "Download new versions of songs in your collection",
		Long: `Download new versions of songs in your collection (Ignition's "Updates"
list). Files riffgrep downloaded before are replaced; older ones from
elsewhere are pointed out, not deleted.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			rows, err := c.Search(cmd.Context(), ignition.Query{Lens: ignition.Updated})
			if err != nil {
				return err
			}
			if len(rows) == 0 {
				fmt.Println("Everything in your collection is up to date.")
				return nil
			}
			f.owned, f.allVersions = true, true
			return download(cmd.Context(), c, &f, rows)
		},
	}
	f.register(cmd)
	return cmd
}

func configCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "config [key [value]]",
		Short: "Show or change settings",
		Long: `Show or change settings:

  dlc_dir   Rocksmith's dlc folder (default: found through Steam)
  subdir    folder inside it for downloads, one subfolder per artist
            (default riffgrep; "" for none)
  platform  pc or mac (default: this computer's)
  dd        prefer dynamic difficulty: true or false (default true)
  jobs      downloads at once (default 4)`,
		Example: `  riffgrep config
  riffgrep config dlc_dir "D:\SteamLibrary\steamapps\common\Rocksmith2014\dlc"`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := library.LoadConfig()
			if err != nil {
				return err
			}
			if len(args) < 2 {
				dir, _ := library.Dir()
				dlc, derr := cfg.FindDlcDir()
				if derr != nil {
					dlc = "not found"
				}
				platform := cfg.Platform
				if platform == "" {
					platform = string(library.HostPlatform()) + " (this computer)"
				}
				vals := map[string]string{
					"dlc_dir":  orDefault(cfg.DlcDir, dlc),
					"subdir":   strconv.Quote(cfg.Subdir),
					"platform": platform,
					"dd":       strconv.FormatBool(cfg.DD),
					"jobs":     strconv.Itoa(cfg.Jobs),
				}
				if len(args) == 1 {
					v, ok := vals[args[0]]
					if !ok {
						return fmt.Errorf("unknown setting %q", args[0])
					}
					fmt.Println(v)
					return nil
				}
				for _, k := range []string{"dlc_dir", "subdir", "platform", "dd", "jobs"} {
					fmt.Printf("%-9s %s\n", k, vals[k])
				}
				fmt.Printf("\nconfig: %s\n", dir)
				return nil
			}
			k, v := args[0], args[1]
			switch k {
			case "dlc_dir":
				cfg.DlcDir = v
			case "subdir":
				cfg.Subdir = v
			case "platform":
				v = strings.ToLower(v)
				if v != "pc" && v != "mac" && v != "" {
					return fmt.Errorf("platform: pc or mac")
				}
				cfg.Platform = v
			case "dd":
				b, err := strconv.ParseBool(v)
				if err != nil {
					return fmt.Errorf("dd: true or false")
				}
				cfg.DD = b
			case "jobs":
				n, err := strconv.Atoi(v)
				if err != nil || n < 1 {
					return fmt.Errorf("jobs: a number from 1")
				}
				cfg.Jobs = n
			default:
				return fmt.Errorf("unknown setting %q", k)
			}
			return cfg.Save()
		},
	}
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
