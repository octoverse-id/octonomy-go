package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
)

// vectorFile is the transport behaviour vector file both lines run (#104). It
// is written on main, and this line keeps a copy that must equal main's at the
// commit baselines.pin names, byte for byte.
//
// That identity is the whole point of the file. Each line runs it with a
// binding of its own (behaviorvectors_test.go), and a binding can only pass or
// fail what its copy says -- so a copy edited here, or left behind when main
// changes a rule, would have each line passing its own version of the rules and
// the drift the vectors exist to catch going unseen. It is checked here, with
// the parity check, because both answer one question about the same pinned
// commit: does this line still match main there? Advancing the pin therefore
// brings main's vectors with it, and this line's binding has to pass them.
const vectorFile = "testdata/behavior_vectors.json"

// vectorFindings compares this tree's vector file with main's at the pin. Any
// difference is a finding, never a skip: a pin that predates the file, a tree
// without it, and a single differing byte all fail.
func vectorFindings(r *repo, top, mainPin string) ([]string, error) {
	spec := mainPin + ":" + vectorFile
	if _, err := r.run("cat-file", "-e", spec); err != nil {
		return []string{fmt.Sprintf("vectors: main at %s has no %s -- advance the pin to a commit that has it (a port of everything the newer commit brings), since this line runs main's vectors.", mainPin, vectorFile)}, nil
	}
	// cat-file blob, through gitRaw: the exact bytes, untrimmed and unfiltered.
	want, err := r.gitRaw(r.dir, "cat-file", "blob", spec)
	if err != nil {
		return nil, cannot("%v", err)
	}
	got, err := os.ReadFile(filepath.Join(top, filepath.FromSlash(vectorFile)))
	if os.IsNotExist(err) {
		return []string{fmt.Sprintf("vectors: this tree has no %s -- copy main's at %s (`git show %s > %s`).", vectorFile, mainPin, spec, vectorFile)}, nil
	}
	if err != nil {
		return nil, cannot("%v", err)
	}
	if !bytes.Equal(got, []byte(want)) {
		return []string{fmt.Sprintf("vectors: %s differs from main's at %s, first at line %d -- this line runs main's vectors unchanged; take main's file (`git show %s > %s`), and change a rule on main first.", vectorFile, mainPin, firstDifferingLine(got, []byte(want)), spec, vectorFile)}, nil
	}
	return nil, nil
}

// firstDifferingLine is the 1-based line on which a and b first differ, so a
// finding points at the vector that moved rather than at the whole file.
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
