// Package library knows where things go: riffgrep's config and login, the
// record of what it downloaded, and Rocksmith 2014's dlc folder.
package library

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/muradbu/riffgrep/internal/pick"
)

// Dir is riffgrep's config directory: ~/Library/Application Support/riffgrep
// on macOS, %AppData%\riffgrep on Windows, ~/.config/riffgrep elsewhere.
func Dir() (string, error) {
	if d := os.Getenv("RIFFGREP_CONFIG_DIR"); d != "" {
		return d, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "riffgrep"), nil
}

type Config struct {
	// DlcDir is Rocksmith's dlc folder; empty: found through Steam.
	DlcDir string `json:"dlc_dir,omitempty"`
	// Subdir under DlcDir that downloads go into, one folder per artist.
	Subdir string `json:"subdir"`
	// Platform is "pc" or "mac"; empty: the one riffgrep runs on.
	Platform string `json:"platform,omitempty"`
	DD       bool   `json:"dd"`
	Jobs     int    `json:"jobs"`
}

func defaults() Config {
	return Config{Subdir: "riffgrep", DD: true, Jobs: 4}
}

func LoadConfig() (Config, error) {
	c := defaults()
	dir, err := Dir()
	if err != nil {
		return c, err
	}
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("config.json: %w", err)
	}
	return c, nil
}

func (c Config) Save() error {
	return writeJSON("config.json", c)
}

func (c Config) Prefs() pick.Prefs {
	p := pick.Prefs{DD: c.DD, Platform: pick.Platform(strings.ToLower(c.Platform))}
	if p.Platform == pick.Unknown {
		p.Platform = HostPlatform()
	}
	return p
}

func HostPlatform() pick.Platform {
	if runtime.GOOS == "darwin" {
		return pick.Mac
	}
	return pick.PC
}

// Cookie is the saved Ignition login (RIFFGREP_COOKIE overrides it).
func Cookie() (string, error) {
	if c := os.Getenv("RIFFGREP_COOKIE"); c != "" {
		return c, nil
	}
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(filepath.Join(dir, "cookie"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return strings.TrimSpace(string(b)), err
}

func SaveCookie(cookie string) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "cookie"), []byte(cookie+"\n"), 0o600)
}

// DlcDir is where Rocksmith looks for songs: the configured folder, or the
// game's dlc folder in whichever Steam library has it installed.
func (c Config) FindDlcDir() (string, error) {
	if c.DlcDir != "" {
		return c.DlcDir, nil
	}
	var tried []string
	for _, root := range steamRoots() {
		for _, lib := range steamLibraries(root) {
			d := filepath.Join(lib, "steamapps", "common", "Rocksmith2014", "dlc")
			tried = append(tried, d)
			if st, err := os.Stat(d); err == nil && st.IsDir() {
				return d, nil
			}
		}
	}
	return "", fmt.Errorf("Rocksmith 2014's dlc folder not found (looked in %s); set it with `riffgrep config dlc_dir <folder>` or pass --dest",
		strings.Join(tried, ", "))
}

func steamRoots() []string {
	home, _ := os.UserHomeDir()
	roots := platformSteamRoots()
	switch runtime.GOOS {
	case "darwin":
		roots = append(roots, filepath.Join(home, "Library", "Application Support", "Steam"))
	case "windows":
		for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles"} {
			if p := os.Getenv(env); p != "" {
				roots = append(roots, filepath.Join(p, "Steam"))
			}
		}
	default:
		roots = append(roots, filepath.Join(home, ".steam", "steam"), filepath.Join(home, ".local", "share", "Steam"))
	}
	return roots
}

var vdfPath = regexp.MustCompile(`"path"\s+"([^"]+)"`)

// steamLibraries reads a Steam install's library list (the install itself
// is always one).
func steamLibraries(root string) []string {
	libs := []string{root}
	b, err := os.ReadFile(filepath.Join(root, "steamapps", "libraryfolders.vdf"))
	if err != nil {
		return libs
	}
	for _, m := range vdfPath.FindAllStringSubmatch(string(b), -1) {
		p := strings.ReplaceAll(m[1], `\\`, `\`)
		if !strings.EqualFold(filepath.Clean(p), filepath.Clean(root)) {
			libs = append(libs, p)
		}
	}
	return libs
}

// State is what riffgrep downloaded, per Ignition CDLC ID, so updates can
// replace the old files and failed downloads aren't taken as owned.
type State struct {
	CDLC   map[int]Entry  `json:"cdlc"`
	Failed map[int]string `json:"failed"`
}

type Entry struct {
	Artist  string    `json:"artist"`
	Title   string    `json:"title"`
	Version string    `json:"version"`
	Files   []string  `json:"files"`
	At      time.Time `json:"at"`
}

func LoadState() (*State, error) {
	s := &State{CDLC: map[int]Entry{}, Failed: map[int]string{}}
	dir, err := Dir()
	if err != nil {
		return s, err
	}
	b, err := os.ReadFile(filepath.Join(dir, "library.json"))
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, s); err != nil {
		return s, fmt.Errorf("library.json: %w", err)
	}
	if s.CDLC == nil {
		s.CDLC = map[int]Entry{}
	}
	if s.Failed == nil {
		s.Failed = map[int]string{}
	}
	return s, nil
}

func (s *State) Save() error {
	return writeJSON("library.json", s)
}

func writeJSON(name string, v any) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p+".tmp", append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(p+".tmp", p)
}
