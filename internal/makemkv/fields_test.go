package makemkv

import (
	"reflect"
	"testing"
)

func TestSplitFields(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"plain", `5011,0,0`, []string{"5011", "0", "0"}},
		{"quoted values lose their quotes", `0,2,0,"Fate/Zero"`, []string{"0", "2", "0", "Fate/Zero"}},
		{"escaped quotes and a comma in quotes", `5010,0,1,"a, \"b\"","fmt %1","x"`, []string{"5010", "0", "1", `a, "b"`, "fmt %1", "x"}},
		{"escaped backslash", `"a\\b"`, []string{`a\b`}},
		{"empty fields", `1,,3`, []string{"1", "", "3"}},
		{"empty quoted field", `1,"",3`, []string{"1", "", "3"}},
		{"trailing comma", `1,2,`, []string{"1", "2", ""}},
		{"empty input", ``, []string{""}},
		{"lone trailing backslash in quotes is kept", `"a\`, []string{`a\`}},
		{"unterminated quote runs to the end", `1,"abc,def`, []string{"1", "abc,def"}},
		{"utf-8 passes through", `"ブルーレイ",1`, []string{"ブルーレイ", "1"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := SplitFields(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("SplitFields(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
