package audit

import (
	"regexp"
	"strings"
)

// nameSubs and nameDrop are the illegal-character substitutions a filename
// carries while the container title keeps its punctuation, so `Re: Cutie
// Honey` lives in a directory named `Re - Cutie Honey` and both are correct.
// ": " is consumed before a bare ":", or the colon would leave a doubled
// space. "/" becomes a space rather than vanishing, which is how the library
// already spells every Fate entry.
var nameSubs = [][2]string{{": ", " - "}, {":", " - "}, {"?", "？"}, {"/", " "}}

const nameDrop = `*"<>|\`

// asFilename is a title the way a file or directory name spells it.
func asFilename(name string) string {
	for _, s := range nameSubs {
		name = strings.ReplaceAll(name, s[0], s[1])
	}
	name = strings.Map(func(r rune) rune {
		if strings.ContainsRune(nameDrop, r) {
			return -1
		}
		return r
	}, name)
	return strings.Join(strings.Fields(name), " ")
}

// titleMatches reports whether a container title is right for a name: equal to
// it, or equal once spelled as a filename.
func titleMatches(title, name string) bool {
	return title == name || asFilename(title) == name
}

// episodeName is a correctly shaped episode: a plain number, a fractional
// recap number (S01E05.5), and the " (OVA)" suffix the skill mandates for an
// OVA bundled on a TV disc. The extension is any container, because a
// correctly named legacy episode is a naming pass whose only defect is its
// container; callers only match names that are already videos.
var episodeName = regexp.MustCompile(`(?i)^(.+) - S(\d{2})E(\d{2}(?:\.\d+)?)(?: - (.*?))?(?: \(OVA\))?\.[^.]+$`)

var (
	yearInDir = regexp.MustCompile(`\((19|20)\d{2}\)\s*$`)
	s99Canon  = regexp.MustCompile(` - S99E\d{2} - `)
	s99Any    = regexp.MustCompile(`S99E\d{2}`)
)
