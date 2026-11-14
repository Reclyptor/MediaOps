package audit

import "testing"

func TestAsFilename(t *testing.T) {
	for in, want := range map[string]string{
		"Re: Cutie Honey":  "Re - Cutie Honey",
		"A:B":              "A - B",
		"What?":            "What？",
		"Fate/Apocrypha":   "Fate Apocrypha",
		"Fate/Stay Night":  "Fate Stay Night",
		`a*b"c<d>e|f\g`:    "abcdefg",
		"a  b":             "a b",
		" lead and trail ": "lead and trail",
		"Plain":            "Plain",
	} {
		if got := asFilename(in); got != want {
			t.Errorf("asFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTitleMatches(t *testing.T) {
	if !titleMatches("Show", "Show") || !titleMatches("Re: Cutie Honey", "Re - Cutie Honey") ||
		!titleMatches("Fate/Apocrypha", "Fate Apocrypha") {
		t.Error("a title that spells the series, raw or as a filename, must match")
	}
	if titleMatches("", "Show") || titleMatches("Other", "Show") {
		t.Error("an empty or different title must not match")
	}
}

func TestEpisodeName(t *testing.T) {
	good := []string{
		"Show - S01E01 - Pilot.mkv",
		"Show - S01E05.5 - Recap.mkv",
		"Show - S01E03 - Special (OVA).mkv",
		"Show - S01E03 (OVA).mkv",
		"Show - S01E01.ogm",
		"Show - S01E01 - Pilot.AVI",
		"Show - s01e01 - Pilot.mkv",
		"Show - S00E12 - Title.With.Dots.mp4",
	}
	for _, n := range good {
		if !episodeName.MatchString(n) {
			t.Errorf("%q should be a correctly shaped episode", n)
		}
	}
	for _, n := range []string{
		"Show - S1E01 - Pilot.mkv",
		"Show - E01.mkv",
		"Show S01E01.mkv",
		"S01E01 - Pilot.mkv",
		"Show - S01E1 - Pilot.mkv",
		"Show - S01E01 - Pilot",
	} {
		if episodeName.MatchString(n) {
			t.Errorf("%q should not be a correctly shaped episode", n)
		}
	}
	if m := episodeName.FindStringSubmatch("Fate Zero - S02E05.5 - Recap.mkv"); m == nil || m[1] != "Fate Zero" || m[3] != "05.5" {
		t.Errorf("groups = %q", m)
	}
}
