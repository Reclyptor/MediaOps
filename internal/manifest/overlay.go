package manifest

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// entry is something the overlay knows about that the real tree may not hold.
type entry struct {
	dir  bool
	size int64
}

// overlay is the real tree plus the effect of every op replayed so far, so an
// op is judged on the state the ops before it leave behind. It only reads.
// Paths are absolute and clean.
type overlay struct {
	// gone is every path moved or removed; a directory hides all beneath it.
	gone map[string]bool
	// added is every path moved in.
	added map[string]entry
}

func newOverlay() *overlay {
	return &overlay{gone: map[string]bool{}, added: map[string]entry{}}
}

func (o *overlay) hidden(p string) bool {
	for q := p; ; q = filepath.Dir(q) {
		if o.gone[q] {
			return true
		}
		if q == filepath.Dir(q) {
			return false
		}
	}
}

func (o *overlay) isFile(p string) bool {
	if e, ok := o.added[p]; ok {
		return !e.dir
	}
	if o.hidden(p) {
		return false
	}
	_, ok := regularFile(p)
	return ok
}

func (o *overlay) isDir(p string) bool {
	if e, ok := o.added[p]; ok {
		return e.dir
	}
	prefix := p + "/"
	for k := range o.added {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return !o.hidden(p) && realDir(p)
}

func (o *overlay) exists(p string) bool { return o.isFile(p) || o.isDir(p) }

func (o *overlay) size(p string) int64 {
	if e, ok := o.added[p]; ok {
		return e.size
	}
	if info, ok := regularFile(p); ok {
		return info.Size()
	}
	return 0
}

// filesUnder maps every regular file under d, as the replay sees it, to its
// size.
func (o *overlay) filesUnder(d string) map[string]int64 {
	out := map[string]int64{}
	prefix := d + "/"
	for k, e := range o.added {
		if strings.HasPrefix(k, prefix) && !e.dir {
			out[k] = e.size
		}
	}
	if !o.hidden(d) && realDir(d) {
		_ = filepath.WalkDir(d, func(p string, de fs.DirEntry, err error) error {
			if err != nil || !de.Type().IsRegular() {
				return nil
			}
			if _, ok := out[p]; ok || o.hidden(p) {
				return nil
			}
			if info, err := de.Info(); err == nil {
				out[p] = info.Size()
			}
			return nil
		})
	}
	return out
}

// children names what is directly inside d.
func (o *overlay) children(d string) map[string]bool {
	kids := map[string]bool{}
	prefix := d + "/"
	for k := range o.added {
		if rest, ok := strings.CutPrefix(k, prefix); ok {
			kids[strings.SplitN(rest, "/", 2)[0]] = true
		}
	}
	if !o.hidden(d) {
		if entries, err := os.ReadDir(d); err == nil {
			for _, e := range entries {
				if !o.hidden(filepath.Join(d, e.Name())) {
					kids[e.Name()] = true
				}
			}
		}
	}
	return kids
}

func (o *overlay) moveFile(s, d string) {
	size := o.size(s)
	delete(o.added, s)
	o.gone[s] = true
	o.added[d] = entry{size: size}
}

// moveDir moves s to d. With merge the directory d already holds files, as it
// does mid-way through an interrupted move, so d itself is not recorded.
func (o *overlay) moveDir(s, d string, merge bool) {
	files := o.filesUnder(s)
	prefix := s + "/"
	for k := range o.added {
		if k == s || strings.HasPrefix(k, prefix) {
			delete(o.added, k)
		}
	}
	o.gone[s] = true
	if !merge {
		o.added[d] = entry{dir: true}
	}
	for p, size := range files {
		o.added[d+p[len(s):]] = entry{size: size}
	}
}

func (o *overlay) removeDir(d string) {
	delete(o.added, d)
	o.gone[d] = true
}

// hide removes the source of a move the tree already shows as done.
func (o *overlay) hide(p string) {
	delete(o.added, p)
	o.gone[p] = true
}
