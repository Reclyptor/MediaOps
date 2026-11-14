package manifest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestNoRemovalOrRenameInPackage is the structural half of "nothing here
// deletes media": parse the package and fail on any call that can remove or
// truncate a file or rename one. The executor performs every move through
// internal/library.
func TestNoRemovalOrRenameInPackage(t *testing.T) {
	banned := map[string][]string{
		"os":      {"Remove", "RemoveAll", "Truncate", "Rename", "Link", "Symlink"},
		"unix":    {"Unlink", "Unlinkat", "Rmdir", "Truncate", "Ftruncate", "Rename", "Renameat", "Renameat2"},
		"syscall": {"Unlink", "Unlinkat", "Rmdir", "Truncate", "Ftruncate", "Rename", "Renameat"},
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var scanned int
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		scanned++
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && slices.Contains(banned[pkg.Name], sel.Sel.Name) {
				t.Errorf("%s: calls %s.%s", fset.Position(call.Pos()), pkg.Name, sel.Sel.Name)
			}
			if sel.Sel.Name == "Truncate" {
				t.Errorf("%s: calls a Truncate method", fset.Position(call.Pos()))
			}
			return true
		})
	}
	if scanned < 8 {
		t.Fatalf("scanned only %d files; the guard is not looking at the package", scanned)
	}
}
