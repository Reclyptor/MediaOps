package library

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestNoDeletionOutsideRemoveGo is the structural half of "no deletion path
// exists": parse the package and the two commands, and fail on any call that
// can remove or truncate a file, or rename over one, outside the files and
// functions that are allowed to.
func TestNoDeletionOutsideRemoveGo(t *testing.T) {
	removers := map[string][]string{
		"os":      {"Remove", "RemoveAll", "Truncate"},
		"unix":    {"Unlink", "Unlinkat", "Rmdir", "Truncate", "Ftruncate"},
		"syscall": {"Unlink", "Unlinkat", "Rmdir", "Truncate", "Ftruncate"},
	}
	renamers := map[string][]string{"os": {"Rename"}, "unix": {"Rename", "Renameat", "Renameat2"}, "syscall": {"Rename", "Renameat"}}

	// What each file may call, by enclosing function.
	allowed := map[string]map[string]bool{
		"remove.go": {"unlinkSource": true, "discardPartial": true, "removeProbe": true, "rmdirEmpty": true},
		"move.go":   {"rename": true, "copyAcross": true},
	}

	dirs := []string{".", "../../cmd/mkv-archive", "../../cmd/mkv-promote"}
	var scanned int
	for _, dir := range dirs {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
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
			base := filepath.Base(file)
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				ast.Inspect(fn, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					pkg, ok := sel.X.(*ast.Ident)
					if !ok {
						return true
					}
					if slices.Contains(removers[pkg.Name], sel.Sel.Name) && !(base == "remove.go" && allowed[base][fn.Name.Name]) {
						t.Errorf("%s: %s calls %s.%s; removals belong in remove.go", fset.Position(call.Pos()), fn.Name.Name, pkg.Name, sel.Sel.Name)
					}
					if slices.Contains(renamers[pkg.Name], sel.Sel.Name) && !(base == "move.go" && allowed[base][fn.Name.Name]) {
						t.Errorf("%s: %s calls %s.%s; renames belong in move.go", fset.Position(call.Pos()), fn.Name.Name, pkg.Name, sel.Sel.Name)
					}
					return true
				})
			}
		}
	}
	if scanned < 10 {
		t.Fatalf("scanned only %d files; the guard is not looking at the package", scanned)
	}
}

func TestDiscardPartialOnlyRemovesPartials(t *testing.T) {
	dir := t.TempDir()
	media := putFile(t, filepath.Join(dir, "ep.mkv"), "media")
	if err := discardPartial(media); err == nil || !exists(media) {
		t.Fatalf("discardPartial removed a non-partial: %v", err)
	}
	if err := discardPartial(".partial"); err == nil {
		t.Fatal("discardPartial accepted a bare suffix")
	}
	partial := putFile(t, filepath.Join(dir, "ep.mkv.partial"), "half")
	if err := discardPartial(partial); err != nil || exists(partial) {
		t.Fatalf("err = %v", err)
	}
	if err := discardPartial(partial); err != nil {
		t.Fatalf("a partial that is already gone is not an error: %v", err)
	}
}

func TestRemoveProbeOnlyRemovesEmptyFiles(t *testing.T) {
	dir := t.TempDir()
	full := putFile(t, filepath.Join(dir, "full.mkv"), "x")
	if err := removeProbe(full); err == nil || !exists(full) {
		t.Fatalf("removeProbe removed a file with content: %v", err)
	}
	if err := removeProbe(dir); err == nil || !exists(dir) {
		t.Fatalf("removeProbe removed a directory: %v", err)
	}
	empty := putFile(t, filepath.Join(dir, "empty"), "")
	if err := removeProbe(empty); err != nil || exists(empty) {
		t.Fatalf("err = %v", err)
	}
}

func TestRmdirEmptyNeverRemovesAFile(t *testing.T) {
	dir := t.TempDir()
	file := putFile(t, filepath.Join(dir, "ep.mkv"), "media")
	if err := rmdirEmpty(file); err == nil || !exists(file) {
		t.Fatalf("rmdirEmpty touched a file: %v", err)
	}
	full := filepath.Join(dir, "full")
	putFile(t, filepath.Join(full, "ep.mkv"), "media")
	if err := rmdirEmpty(full); err == nil || !exists(full) {
		t.Fatalf("rmdirEmpty removed a directory with content: %v", err)
	}
	empty := filepath.Join(dir, "empty")
	if err := os.Mkdir(empty, 0o770); err != nil {
		t.Fatal(err)
	}
	if err := rmdirEmpty(empty); err != nil || exists(empty) {
		t.Fatalf("err = %v", err)
	}
}
