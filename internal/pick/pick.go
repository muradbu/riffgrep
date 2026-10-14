// Package pick chooses which of a download's files to keep: Rocksmith 2014
// CDLC come as psarc files, often several per song (PC and Mac, with and
// without dynamic difficulty), spread over folders or archives.
package pick

import (
	"path"
	"regexp"
	"sort"
	"strings"
)

type Platform string

const (
	PC      Platform = "pc"
	Mac     Platform = "mac"
	Unknown Platform = ""
)

// Prefs says which variant to keep when a song comes in several.
type Prefs struct {
	Platform Platform // PC or Mac; Unknown keeps both
	DD       bool     // prefer dynamic difficulty; false prefers the non-DD file
}

// Variant is what a file's name and folders say about it.
type Variant struct {
	Platform Platform
	DD       *bool // nil when the name doesn't say
	Song     string
}

var (
	// Rocksmith's own naming: <name>_p.psarc for PC, _m.psarc for Mac.
	platformSuffix = regexp.MustCompile(`(?i)_([pm])\.psarc$`)
	noDD           = regexp.MustCompile(`(?i)(^|[^a-z0-9])(no|non|without|w/?o)[ _.-]*dd([^a-z0-9]|$)|(^|[^a-z0-9])n-?dd([^a-z0-9]|$)`)
	withDD         = regexp.MustCompile(`(?i)(^|[^a-z0-9])(dd|dynamic[ _.-]*difficulty)([^a-z0-9]|$)`)
	macDir         = regexp.MustCompile(`(?i)(^|[^a-z0-9])(mac|macos|osx|apple)([^a-z0-9]|$)`)
	pcDir          = regexp.MustCompile(`(?i)(^|[^a-z0-9])(pc|win|windows|win64)([^a-z0-9]|$)`)
	nonAlnum       = regexp.MustCompile(`[^a-z0-9]+`)
)

func IsPsarc(name string) bool {
	return strings.EqualFold(path.Ext(name), ".psarc")
}

// Classify reads a psarc's path (folders included, "/"-separated).
func Classify(p string) Variant {
	name := path.Base(p)
	dir := path.Dir(p)
	v := Variant{}
	if m := platformSuffix.FindStringSubmatch(name); m != nil {
		v.Platform = map[string]Platform{"p": PC, "m": Mac}[strings.ToLower(m[1])]
	} else if macDir.MatchString(dir) {
		v.Platform = Mac
	} else if pcDir.MatchString(dir) {
		v.Platform = PC
	}
	yes, no := true, false
	switch {
	case noDD.MatchString(name):
		v.DD = &no
	case withDD.MatchString(name):
		v.DD = &yes
	case noDD.MatchString(dir):
		v.DD = &no
	case withDD.MatchString(dir):
		v.DD = &yes
	}
	v.Song = songKey(name)
	return v
}

// songKey is a file name without what tells its variants apart, so the
// variants of one song share it.
func songKey(name string) string {
	name = platformSuffix.ReplaceAllString(name, "")
	name = strings.TrimSuffix(strings.ToLower(name), ".psarc")
	words := strings.Fields(nonAlnum.ReplaceAllString(name, " "))
	var kept []string
	for i, w := range words {
		next := ""
		if i+1 < len(words) {
			next = words[i+1]
		}
		switch {
		case w == "dd" || w == "ndd" || w == "nodd" || w == "nondd":
		case (w == "no" || w == "non" || w == "without" || w == "n") && next == "dd":
		default:
			kept = append(kept, w)
		}
	}
	return strings.Join(kept, " ")
}

// Choose returns the paths to download out of all of a download's files:
// psarcs only, of the preferred platform (or of no stated platform), and
// per song the preferred DD variant when there is a choice.
func Choose(paths []string, prefs Prefs) []string {
	type cand struct {
		path string
		v    Variant
	}
	bySong := map[string][]cand{}
	var order []string
	for _, p := range paths {
		if !IsPsarc(p) {
			continue
		}
		v := Classify(p)
		if prefs.Platform != Unknown && v.Platform != Unknown && v.Platform != prefs.Platform {
			continue
		}
		key := v.Song + "\x00" + string(v.Platform)
		if _, ok := bySong[key]; !ok {
			order = append(order, key)
		}
		bySong[key] = append(bySong[key], cand{p, v})
	}
	score := func(v Variant) int {
		switch {
		case v.DD == nil:
			return 1
		case *v.DD == prefs.DD:
			return 2
		default:
			return 0
		}
	}
	var out []string
	for _, key := range order {
		cs := bySong[key]
		best := 0
		for _, c := range cs {
			best = max(best, score(c.v))
		}
		for _, c := range cs {
			if score(c.v) == best {
				out = append(out, c.path)
			}
		}
	}
	sort.Strings(out)
	return out
}
