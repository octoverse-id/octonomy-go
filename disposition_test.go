package octonomy

import (
	"io/ioutil"
	"path/filepath"
	"strings"
	"testing"
)

// dispositionTable is the per-file record of what became of main's tests on
// this line (#95).
const dispositionTable = "docs/compat-test-disposition.md"

// Every test file in this package is named in the disposition table.
//
// The table's main side is pinned to one commit and cannot drift, but its side
// for this line moves every time a port lands. What this catches is the drift
// that happens without anyone deciding it: a port that lands under a name the
// table does not know, or a new compat-only file that nobody gave a row. It
// cannot tell whether a row's verdict is still true; the PR that moves a file
// is what updates its row.
func TestDispositionTableNamesEveryTestFile(t *testing.T) {
	raw, err := ioutil.ReadFile(filepath.FromSlash(dispositionTable))
	if err != nil {
		t.Fatalf("read %s: %v", dispositionTable, err)
	}
	table := string(raw)

	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no test files found; the check would pass vacuously")
	}
	for _, name := range files {
		if !strings.Contains(table, "`"+name+"`") {
			t.Errorf("%s is not named in %s. Give it a row -- a port of a main file under its "+
				"verdict, or a compat-only file in the table of files with no main counterpart",
				name, dispositionTable)
		}
	}
}
