// Package makemkv knows makemkvcon's robot-mode output and the way the toolset
// invokes it: the line parser, the argv for both passes and the licence file.
package makemkv

import "strings"

// SplitFields splits a robot-mode field list: commas outside quotes separate
// fields, a backslash escapes the next byte inside quotes, and the quotes
// themselves are dropped. Empty fields and a trailing comma yield empty strings.
func SplitFields(s string) []string {
	var (
		fields []string
		cur    strings.Builder
		quoted bool
	)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quoted && c == '\\' && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
		case quoted && c == '"':
			quoted = false
		case quoted:
			cur.WriteByte(c)
		case c == '"':
			quoted = true
		case c == ',':
			fields = append(fields, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	return append(fields, cur.String())
}
