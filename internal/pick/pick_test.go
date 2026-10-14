package pick

import (
	"reflect"
	"testing"
)

func TestClassify(t *testing.T) {
	yes, no := true, false
	for _, c := range []struct {
		path     string
		platform Platform
		dd       *bool
		song     string
	}{
		{"Metallica_One_v2_DD_p.psarc", PC, &yes, "metallica one v2"},
		{"Metallica_One_v2_m.psarc", Mac, nil, "metallica one v2"},
		{"Metallica_One_v2_NDD_p.psarc", PC, &no, "metallica one v2"},
		{"Metallica_One_v2_No-DD_p.psarc", PC, &no, "metallica one v2"},
		{"Mac/DD/Tool-Schism.psarc", Mac, &yes, "tool schism"},
		{"Windows/Non DD/Tool-Schism.psarc", PC, &no, "tool schism"},
		{"Radiohead_No-Surprises_p.psarc", PC, nil, "radiohead no surprises"},
		{"Added_Bonus_p.psarc", PC, nil, "added bonus"},
	} {
		v := Classify(c.path)
		if v.Platform != c.platform || !reflect.DeepEqual(v.DD, c.dd) || v.Song != c.song {
			t.Errorf("Classify(%q) = %v %v %q, want %v %v %q", c.path, v.Platform, v.DD, v.Song, c.platform, c.dd, c.song)
		}
	}
}

func TestChoose(t *testing.T) {
	files := []string{
		"Album/Song1_DD_p.psarc",
		"Album/Song1_DD_m.psarc",
		"Album/Song1_NDD_p.psarc",
		"Album/Song1_NDD_m.psarc",
		"Album/Song2_p.psarc",
		"Album/Song2_m.psarc",
		"Album/readme.txt",
	}
	got := Choose(files, Prefs{Platform: PC, DD: true})
	want := []string{"Album/Song1_DD_p.psarc", "Album/Song2_p.psarc"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PC, DD: got %v, want %v", got, want)
	}
	got = Choose(files, Prefs{Platform: Mac, DD: false})
	want = []string{"Album/Song1_NDD_m.psarc", "Album/Song2_m.psarc"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Mac, no DD: got %v, want %v", got, want)
	}
	// Only non-DD on offer: still taken.
	got = Choose([]string{"x_NDD_p.psarc"}, Prefs{Platform: PC, DD: true})
	if !reflect.DeepEqual(got, []string{"x_NDD_p.psarc"}) {
		t.Errorf("fallback to non-DD: got %v", got)
	}
}
