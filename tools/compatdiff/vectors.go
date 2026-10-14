package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// vectorFile is the transport behaviour vector file both lines run (#104). It
// is written on main, and this line keeps a copy that must equal main's at the
// commit baselines.pin names, byte for byte as committed.
//
// That identity is the whole point of the file. Each line runs it with a
// binding of its own (behaviorvectors_test.go), and a binding can only pass or
// fail what its copy says -- so a copy edited here, or left behind when main
// changes a rule, would have each line passing its own version of the rules and
// the drift the vectors exist to catch going unseen. It is checked here, with
// the parity check, because both answer one question about the same pinned
// commit: does this line still match main there? Advancing the pin therefore
// brings main's vectors with it, and this line's binding has to pass it.
const vectorFile = "testdata/behavior_vectors.json"

// vectorFindings compares this tree's vector file with main's at the pin. A
// pin that predates the file, a tree without it, and a copy that would not
// commit as main's blob are findings; a git command that fails is not a finding
// but a check that could not be made, so it is never reported as an absence.
//
// The comparison is by OBJECT ID, not by the work-tree bytes: main's side is
// the blob id ls-tree reports at the pin, and this side is what `git
// hash-object --path` makes of the work-tree file -- the id it would get if
// committed, after the same attributes and line-ending conversion a commit
// applies. Comparing raw bytes would fail an identical copy that a checkout
// converted (core.autocrlf on Windows turns LF into CRLF), and comparing HEAD's
// blob would ignore an uncommitted edit, which the rest of this tool reads as
// part of the tree.
func vectorFindings(r *repo, top, mainPin string) ([]string, error) {
	listing, err := r.run("ls-tree", mainPin, "--", vectorFile)
	if err != nil {
		return nil, cannot("%v", err)
	}
	if listing == "" {
		return []string{fmt.Sprintf("vectors: main at %s has no %s -- advance the pin to a commit that has it (a port of everything the newer commit brings), since this line runs main's vectors.", mainPin, vectorFile)}, nil
	}
	want, err := blobID(listing)
	if err != nil {
		return nil, cannot("ls-tree %s %s: %v", mainPin, vectorFile, err)
	}

	path := filepath.Join(top, filepath.FromSlash(vectorFile))
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return []string{fmt.Sprintf("vectors: this tree has no %s -- copy main's at %s (`git show %s:%s > %s`).", vectorFile, mainPin, mainPin, vectorFile, vectorFile)}, nil
	} else if err != nil {
		return nil, cannot("%v", err)
	}
	got, err := r.run("hash-object", "--path="+vectorFile, "--", path)
	if err != nil {
		return nil, cannot("%v", err)
	}
	if got == want {
		return nil, nil
	}
	where := ""
	if line, ok := firstDifferingLineAgainst(r, want, path); ok {
		where = fmt.Sprintf(", first at line %d", line)
	}
	return []string{fmt.Sprintf("vectors: %s differs from main's at %s%s -- this line runs main's vectors unchanged; take main's file (`git show %s:%s > %s`), and change a rule on main first.", vectorFile, mainPin, where, mainPin, vectorFile, vectorFile)}, nil
}

// blobID reads the object id out of one ls-tree line, "<mode> blob <id>\t<path>",
// and refuses anything that is not a blob (a directory, a submodule) rather than
// comparing an id of the wrong kind.
func blobID(listing string) (string, error) {
	if strings.Contains(listing, "\n") {
		return "", fmt.Errorf("more than one entry: %q", listing)
	}
	fields := strings.Fields(strings.SplitN(listing, "\t", 2)[0])
	if len(fields) != 3 || fields[1] != "blob" {
		return "", fmt.Errorf("not a blob: %q", listing)
	}
	return fields[2], nil
}

// firstDifferingLineAgainst says where the work-tree file first departs from
// main's blob, so a finding points at the vector that moved. It is a pointer,
// not the verdict -- the ids already decided that -- so when main's bytes cannot
// be read (a partial clone without the blob, offline) the finding goes without
// it rather than failing.
//
// Main's side is its blob as a checkout of this repository would write it
// (cat-file --filters), so the two are compared in the same form: a converted
// checkout with a real edit points at the edit, and a copy whose only
// difference is its line endings points at the first line.
func firstDifferingLineAgainst(r *repo, blob, path string) (int, bool) {
	want, err := r.gitRaw(r.dir, "cat-file", "--filters", "--path="+vectorFile, blob)
	if err != nil {
		return 0, false
	}
	got, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	return firstDifferingLine(got, []byte(want)), true
}

// firstDifferingLine is the 1-based line on which a and b first differ.
func firstDifferingLine(a, b []byte) int {
	line := 1
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return line
		}
		if a[i] == '\n' {
			line++
		}
	}
	return line
}
