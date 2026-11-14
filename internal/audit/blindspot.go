package audit

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// BlindRow is one series the mkv-only audits could not see all of.
type BlindRow struct {
	Code       string         `json:"code"`
	Path       string         `json:"path"`
	Detail     string         `json:"detail"`
	Series     string         `json:"series"`
	MKV        int            `json:"mkv"`
	Other      int            `json:"other"`
	Loose      int            `json:"loose"`
	Extensions map[string]int `json:"extensions"`
}

// BlindReport is the blindspot audit over a set of category roots.
type BlindReport struct {
	Roots    []string   `json:"roots"`
	Findings []BlindRow `json:"findings"`
	Clean    bool       `json:"clean"`
}

// AuditBlindspot lists every series holding media an mkv filter hides:
// non-Matroska video, or video loose in the series root rather than a Season
// directory. Counts come from walking the tree, never from a manifest, because
// trusting the map is what hid 25 .ogm episodes of Berserk.
func AuditBlindspot(roots []string) (BlindReport, error) {
	rep := BlindReport{Roots: roots, Findings: []BlindRow{}}
	for _, root := range roots {
		series, err := subdirs(root)
		if err != nil {
			return BlindReport{}, err
		}
		for _, s := range series {
			dir := filepath.Join(root, s.Name())
			tree, err := walkVideos(dir)
			if err != nil {
				return BlindReport{}, err
			}
			row := BlindRow{Code: "BLINDSPOT", Path: dir, Series: s.Name(), Extensions: map[string]int{}}
			for _, f := range tree.files {
				ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(f)), ".")
				if ext == "mkv" {
					row.MKV++
				} else {
					row.Other++
					row.Extensions[ext]++
				}
				if filepath.Dir(f) == dir {
					row.Loose++
				}
			}
			if row.Other == 0 && row.Loose == 0 {
				continue
			}
			row.Detail = fmt.Sprintf("%d mkv, %d non-mkv video (%s), %d loose in the series root", row.MKV, row.Other, extList(row.Extensions), row.Loose)
			rep.Findings = append(rep.Findings, row)
		}
	}
	rep.Clean = len(rep.Findings) == 0
	return rep, nil
}

func extList(exts map[string]int) string {
	parts := make([]string, 0, len(exts))
	for e, n := range exts {
		parts = append(parts, fmt.Sprintf("%s:%d", e, n))
	}
	sort.Strings(parts)
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ",")
}
