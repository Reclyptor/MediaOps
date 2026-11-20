package mkvtool

import (
	"bytes"
	"cmp"
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/reclyptor/mediaops/kit/proc"
)

// node is any XML element. It keeps unknown elements, attributes and text, so a
// tag file read and written back loses nothing it carried.
type node struct {
	XMLName  xml.Name
	Attrs    []xml.Attr `xml:",any,attr"`
	Children []node     `xml:",any"`
	Text     string     `xml:",chardata"`
}

func (n *node) child(name string) *node {
	for i := range n.Children {
		if n.Children[i].XMLName.Local == name {
			return &n.Children[i]
		}
	}
	return nil
}

func (n *node) childText(name string) (string, bool) {
	c := n.child(name)
	if c == nil {
		return "", false
	}
	return c.Text, true
}

func (n *node) removeChildren(keep func(*node) bool) {
	n.Children = slices.DeleteFunc(n.Children, func(c node) bool { return !keep(&c) })
}

// dropIndentation clears the whitespace the XML was pretty-printed with, so a
// node written back does not grow stray text between its children.
func (n *node) dropIndentation() {
	if len(n.Children) > 0 && strings.TrimSpace(n.Text) == "" {
		n.Text = ""
	}
	for i := range n.Children {
		n.Children[i].dropIndentation()
	}
}

// tagPair is a top-level Simple tag's name and value.
type tagPair struct{ Name, String string }

func sortPairs(p []tagPair) {
	slices.SortFunc(p, func(a, b tagPair) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.String, b.String))
	})
}

// trackUIDs returns the TrackUID values of a Tag element's Targets.
func trackUIDs(tag *node) []string {
	targets := tag.child("Targets")
	if targets == nil {
		return nil
	}
	var uids []string
	for _, c := range targets.Children {
		if c.XMLName.Local == "TrackUID" {
			uids = append(uids, strings.TrimSpace(c.Text))
		}
	}
	return uids
}

// pairs lists the top-level Simple tags of tags, which must be Tag elements.
// A Simple without a Name is skipped, as mkvpropedit would not write it.
func pairs(tags []*node) []tagPair {
	var out []tagPair
	for _, tag := range tags {
		for i := range tag.Children {
			s := &tag.Children[i]
			if s.XMLName.Local != "Simple" {
				continue
			}
			name, ok := s.childText("Name")
			if !ok {
				continue
			}
			value, _ := s.childText("String")
			out = append(out, tagPair{name, value})
		}
	}
	sortPairs(out)
	return out
}

// extractTags reads every tag of file with mkvextract. No tags at all is an
// empty result, not an error.
func extractTags(ctx context.Context, r proc.Runner, file string) (*node, error) {
	res, err := r.Run(ctx, "mkvextract", file, "tags", "/dev/stdout")
	if err != nil {
		return nil, fmt.Errorf("running mkvextract on %s: %w", file, err)
	}
	if res.ExitCode >= 2 || res.ExitCode < 0 {
		return nil, fmt.Errorf("mkvextract failed on %s (exit %d): %s", file, res.ExitCode, tail(res.Stdout, res.Stderr))
	}
	body := bytes.TrimSpace(bytes.TrimPrefix(res.Stdout, []byte("\xef\xbb\xbf")))
	root := &node{XMLName: xml.Name{Local: "Tags"}}
	if len(body) == 0 {
		return root, nil
	}
	if err := xml.Unmarshal(body, root); err != nil {
		return nil, fmt.Errorf("mkvextract printed unreadable tags for %s: %w", file, err)
	}
	if root.XMLName.Local != "Tags" {
		return nil, fmt.Errorf("mkvextract printed a <%s> document for %s, not <Tags>", root.XMLName.Local, file)
	}
	root.dropIndentation()
	return root, nil
}

// tagsOf returns the Tag elements of root that target uid.
func tagsOf(root *node, uid string) []*node {
	var mine []*node
	for i := range root.Children {
		tag := &root.Children[i]
		if tag.XMLName.Local == "Tag" && slices.Contains(trackUIDs(tag), uid) {
			mine = append(mine, tag)
		}
	}
	return mine
}

// retagLanguage rewrites the Matroska LANGUAGE track tag of the track of type
// tt numbered ordinal (UID uid) to lang. ffmpeg-based readers, Plex among them,
// let that tag override the track's Language element, so a track that carries
// one would keep showing its old language. A track without such a tag is left
// alone. Everything else the track's tags carry is written back unchanged and
// read back by name and value.
func retagLanguage(ctx context.Context, r proc.Runner, file string, tt trackType, ordinal int, uid, lang string) error {
	root, err := extractTags(ctx, r, file)
	if err != nil {
		return err
	}
	mine := tagsOf(root, uid)
	if !hasLanguageTag(mine) {
		return nil
	}

	for _, tag := range mine {
		if n := len(trackUIDs(tag)); n != 1 {
			return fmt.Errorf("a tag of track %s in %s targets %d tracks; rewriting it would change the others", uid, file, n)
		}
		tag.child("Targets").removeChildren(func(c *node) bool { return c.XMLName.Local != "TrackUID" })
		tag.removeChildren(func(c *node) bool {
			if c.XMLName.Local != "Simple" {
				return true
			}
			name, _ := c.childText("Name")
			return name != ""
		})
		for i := range tag.Children {
			s := &tag.Children[i]
			if name, _ := s.childText("Name"); s.XMLName.Local == "Simple" && name == "LANGUAGE" {
				setString(s, lang)
			}
		}
	}
	want := pairs(mine)

	doc, err := xml.Marshal(&node{XMLName: xml.Name{Local: "Tags"}, Children: derefNodes(mine)})
	if err != nil {
		return fmt.Errorf("writing the tags of track %s in %s: %w", uid, file, err)
	}
	if err := writeTags(ctx, r, file, tt, ordinal, append([]byte(`<?xml version="1.0"?>`), doc...)); err != nil {
		return err
	}

	back, err := extractTags(ctx, r, file)
	if err != nil {
		return err
	}
	if got := pairs(tagsOf(back, uid)); !slices.Equal(got, want) {
		return fmt.Errorf("the tags of %s track %d of %s read back as %v after writing %v", tt.name, ordinal, file, got, want)
	}
	return nil
}

func hasLanguageTag(tags []*node) bool {
	for _, p := range pairs(tags) {
		if p.Name == "LANGUAGE" {
			return true
		}
	}
	return false
}

func setString(simple *node, value string) {
	if s := simple.child("String"); s != nil {
		s.Text = value
		return
	}
	simple.Children = append(simple.Children, node{XMLName: xml.Name{Local: "String"}, Text: value})
}

func derefNodes(in []*node) []node {
	out := make([]node, len(in))
	for i, n := range in {
		out[i] = *n
	}
	return out
}

// writeTags hands doc to mkvpropedit as the tags of track ordinal of type tt. The
// temporary file is removed whatever happens.
func writeTags(ctx context.Context, r proc.Runner, file string, tt trackType, ordinal int, doc []byte) (err error) {
	f, err := os.CreateTemp("", "mkvtool-tags-*.xml")
	if err != nil {
		return fmt.Errorf("creating a temporary tags file: %w", err)
	}
	defer func() {
		if rmErr := os.Remove(f.Name()); rmErr != nil && !os.IsNotExist(rmErr) && err == nil {
			err = fmt.Errorf("removing %s: %w", f.Name(), rmErr)
		}
	}()
	_, werr := f.Write(doc)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return fmt.Errorf("writing %s: %w", f.Name(), werr)
	}
	return runEdit(ctx, r, file, "mkvpropedit", file, "--tags", fmt.Sprintf("%s:%s", tt.selector(ordinal), f.Name()))
}
