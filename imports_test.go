package pdfo

import (
	"bufio"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// forbidden are the imports that could reach files, the network or the
// clock, or bind the core to one platform. The standard library imports some
// of them itself (fmt imports os, crypto imports syscall), which no Go code
// can avoid; what this test guarantees is that the code of this module never
// calls them.
var forbidden = []string{
	"os", "os/exec", "os/signal", "os/user", "io/fs", "io/ioutil",
	"path/filepath", "syscall", "syscall/js", "time", "net", "embed",
	"plugin", "unsafe", "runtime/cgo", "C",
}

// TestImports checks every non-test file of the core packages: the root and
// everything under internal/ except the test helpers.
func TestImports(t *testing.T) {
	fset := token.NewFileSet()
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch path {
			case "testdata", "wasm", ".git", filepath.Join("internal", "pdftest"):
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			for _, bad := range forbidden {
				if p == bad || strings.HasPrefix(p, "net/") {
					t.Errorf("%s imports %s", path, p)
				}
			}
			if !strings.Contains(strings.Split(p, "/")[0], ".") {
				continue // standard library
			}
			if p != "github.com/wisebrian/pdfo" && !strings.HasPrefix(p, "github.com/wisebrian/pdfo/") {
				t.Errorf("%s imports %s, which is not in the standard library", path, p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestGoMod checks that the module requires nothing.
func TestGoMod(t *testing.T) {
	f, err := os.Open("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		if strings.HasPrefix(strings.TrimSpace(s.Text()), "require") {
			t.Errorf("go.mod requires a module: %s", s.Text())
		}
	}
}
