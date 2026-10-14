# riffgrep

Search [CustomsForge Ignition](https://ignition4.customsforge.com) from the
command line and download Rocksmith 2014 CDLC straight into the game's `dlc`
folder, a whole artist or album at once. Runs on macOS and Windows (and
Linux).

riffgrep follows each chart's download link wherever the creator put it
(Google Drive files and folders, Dropbox files and folders, MediaFire,
MEGA, direct links), unpacks zip/rar/7z archives, and keeps only the file for
your platform (`_p.psarc` for PC, `_m.psarc` for Mac), with dynamic
difficulty when a chart comes with and without.

```
riffgrep search master of puppets
riffgrep artist "Royal Blood"
riffgrep album "Master of Puppets" -a Metallica
riffgrep get 20769 50913
riffgrep updates
```

## Install

With Go 1.26+: `go install github.com/muradbu/riffgrep@latest`.

## Log in

Ignition needs your CustomsForge login. riffgrep uses the cookie your browser
keeps you logged in with, never your password:

1. Log in at https://ignition4.customsforge.com.
2. Open the developer tools (F12, or ⌥⌘I on a Mac): **Application** (Chrome,
   Edge) or **Storage** (Firefox, Safari) → **Cookies** →
   `https://ignition4.customsforge.com`.
3. Copy the **Value** of the cookie named `remember_web_…`, then run
   `riffgrep login` and paste it.

It lasts about a year; logging out on the site ends it. It's stored in
riffgrep's config folder (`riffgrep config` shows where).

## Downloading

`artist` and `album` take one chart per song, the most downloaded one, skip
whole-album charts, and skip songs already in your Ignition collection
(`--owned` to include them, `--all-versions` for every chart). `-n` lists
what would be downloaded without downloading.

Files go into `<dlc>/riffgrep/<Artist>/`. The `dlc` folder is found through
Steam; set it with `riffgrep config dlc_dir <folder>` if not, or download
elsewhere with `--dest`. Rocksmith needs the
[CDLC Enabler](https://ignition4.customsforge.com/tools/cdlcenabler) to load
custom songs.

Downloads count on Ignition like the site's own button: each chart is added
to your collection, so `riffgrep updates` can fetch new versions later (and
replaces the files riffgrep downloaded before).

Ignition sometimes asks for a quick human check before more downloads.
riffgrep then stops and prints the remaining IDs: pass the check in your
browser and continue with `riffgrep get <ids>`.

Links riffgrep can't download from (OneDrive, Box, removed files) are listed
at the end with their pages, to get in a browser.

## Settings

`riffgrep config` shows them; `riffgrep config <key> <value>` changes one:

| Key | |
|---|---|
| `dlc_dir` | Rocksmith's `dlc` folder (default: found through Steam) |
| `subdir` | folder inside it for downloads (default `riffgrep`) |
| `platform` | `pc` or `mac` (default: this computer's) |
| `dd` | prefer dynamic difficulty (default `true`) |
| `jobs` | downloads at once (default 4) |

## Development

`go test ./...` runs offline. `RIFFGREP_LIVE=1 go test ./internal/ignition`
queries Ignition with your saved login; `RIFFGREP_LINKS="<link> <link>" go
test ./internal/fetch` downloads from real hosts.
