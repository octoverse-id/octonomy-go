// Command compatdiff compares this line's exported API with main's and with
// this line's published releases, and writes COMPAT.md from the answer (#100).
//
// It is the acceptance artifact of the parity epic (#88): the epic is done when
// COMPAT.md lists only deliberate differences, and this is what makes that
// statement checkable rather than asserted. Two checks, because one cannot do
// both jobs:
//
//	A  parity        main at the commit baselines.pin names, against the work
//	                 tree. Every difference must be listed in carveouts.txt,
//	                 under the section that explains it, and every listed
//	                 difference must still exist.
//	B  compatibility each published release baselines.pin names, against the
//	                 work tree. Every feature a release exported must still be
//	                 here, unchanged. No exceptions: this line can never publish
//	                 a major, so a break has no version to ride.
//
// A comparison of main with this line cannot answer B -- it says nothing about
// what a caller of a release compiled against -- and a symbol-name diff could
// answer neither: it would report parity on a line whose DefaultAPIVersion
// was main's, or whose field changed type. So both compare FEATURES, the
// declaration-level lines api.go prints: types, signatures, constant values,
// struct tags, comparability and interface method sets.
//
// Usage:
//
//	compatdiff [-repo DIR] check   both checks, and COMPAT.md matches what they produce
//	compatdiff [-repo DIR] write   both checks, then write COMPAT.md
//
// `write` writes nothing unless both checks pass, so COMPAT.md is never the
// record of a comparison that failed.
//
// Environment: COMPATDIFF_REMOTE (default origin) and COMPATDIFF_MAIN (default
// main) name where main's branch and the release tags are fetched from when a
// clone lacks them, as scripts/contract-identity.sh's variables do for its pin.
//
// Exit codes: 0 clean, 1 a check found something, 2 a check could not be made.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	pinFile       = "tools/compatdiff/baselines.pin"
	carveoutFile  = "tools/compatdiff/carveouts.txt"
	compatFile    = "COMPAT.md"
	compatCommand = "make compat-md"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("compatdiff", flag.ContinueOnError)
	flags.SetOutput(stderr)
	repoDir := flags.String("repo", ".", "a directory inside the work tree to check")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 1 || (flags.Arg(0) != "check" && flags.Arg(0) != "write") {
		say(stderr, "usage: compatdiff [-repo DIR] check|write\n")
		return 2
	}
	mode := flags.Arg(0)

	findings, rendered, top, err := evaluate(*repoDir, envOr("COMPATDIFF_REMOTE", "origin"), envOr("COMPATDIFF_MAIN", "main"))
	if err != nil {
		report(stderr, "could not check: "+strings.TrimPrefix(err.Error(), errCannot.Error()+": "))
		return 2
	}
	if len(findings) == 0 {
		target := filepath.Join(top, compatFile)
		switch mode {
		case "write":
			if err := os.WriteFile(target, []byte(rendered), 0o644); err != nil {
				report(stderr, "could not write "+compatFile+": "+err.Error())
				return 2
			}
			say(stdout, "compatdiff: both checks pass; wrote %s\n", compatFile)
			return 0
		case "check":
			have, err := os.ReadFile(target)
			switch {
			case errors.Is(err, os.ErrNotExist):
				findings = append(findings, fmt.Sprintf("%s: does not exist -- run `%s` and commit it.", compatFile, compatCommand))
			case err != nil:
				report(stderr, "could not read "+compatFile+": "+err.Error())
				return 2
			case string(have) != rendered:
				findings = append(findings, fmt.Sprintf("%s: is not what the checks produce -- run `%s` and commit the result. It is generated, so an edit made by hand is lost and a stale copy describes a tree that no longer exists.", compatFile, compatCommand))
			}
		}
	}
	if len(findings) != 0 {
		for _, f := range findings {
			report(stdout, f)
		}
		say(stdout, "compatdiff: %d finding(s); %s has the format of the carve-out file\n", len(findings), carveoutFile)
		return 1
	}
	say(stdout, "compatdiff: parity with main and compatibility with every published release hold, and %s is current\n", compatFile)
	return 0
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// report prints one finding, as a GitHub annotation when it runs in Actions so
// it surfaces on the pull request rather than only in the log.
func report(w io.Writer, msg string) {
	if os.Getenv("GITHUB_ACTIONS") != "" {
		msg = strings.ReplaceAll(msg, "\n", "%0A")
		say(w, "::error::compatdiff: %s\n", msg)
		return
	}
	say(w, "compatdiff: %s\n", msg)
}

// say writes one line of output. A write that fails has nowhere left to be
// reported, and the exit code carries the verdict either way.
func say(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

// evaluate runs both checks on the work tree containing dir and returns their
// findings and the COMPAT.md they produce. An error means a check could not be
// made.
func evaluate(dir, remote, mainBranch string) (findings []string, rendered, top string, err error) {
	r, err := openRepo(dir, remote, mainBranch)
	if err != nil {
		return nil, "", "", err
	}
	top = r.dir
	p, err := parsePins(filepath.Join(top, filepath.FromSlash(pinFile)))
	if err != nil {
		return nil, "", "", cannot("%v", err)
	}
	sections, err := parseCarveouts(filepath.Join(top, filepath.FromSlash(carveoutFile)))
	if err != nil {
		return nil, "", "", cannot("%v", err)
	}

	scratch, err := os.MkdirTemp("", "compatdiff-")
	if err != nil {
		return nil, "", "", cannot("%v", err)
	}
	defer func() { _ = os.RemoveAll(scratch) }()

	// The pins first: a pin that is not what it claims to be makes every
	// comparison against it meaningless, so it is a finding of its own.
	if err := r.ensureCommit(p.main, "main's pinned commit"); err != nil {
		return nil, "", "", err
	}
	onMain, err := r.pinOnMain(p.main)
	if err != nil {
		return nil, "", "", err
	}
	if !onMain {
		findings = append(findings, fmt.Sprintf("pin: %s names %s for main, and it is not on %s/%s -- a pin into this line's own history would compare the line with itself.", pinFile, p.main, remote, mainBranch))
	}
	// Every release tag the clone has must be a baseline. A release's own PR
	// cannot name the commit its tag is cut on, so its line arrives in the PR
	// after -- and without this, forgetting that PR would leave Check B
	// holding the tree to every release but the newest, with nothing failing.
	// A fresh CI checkout has every tag. A tag naming HEAD is exempt: that is
	// the tag-push run checking the release itself, whose tree cannot name its
	// own tag, and the release it would hold the tree to is the tree.
	tags, err := r.releaseTags()
	if err != nil {
		return nil, "", "", err
	}
	head, err := r.run("rev-parse", "HEAD")
	if err != nil {
		return nil, "", "", cannot("%v", err)
	}
	pinned := map[string]bool{}
	for _, rel := range p.releases {
		pinned[rel.tag] = true
	}
	for _, tag := range tags {
		if at, err := r.tagCommit(tag); err == nil && at == head {
			continue
		}
		if !pinned[tag] {
			findings = append(findings, fmt.Sprintf("pin: the release tag %s exists and %s does not name it -- add `release %s <its commit>` (docs/release.md), or Check B never holds this line to it.", tag, pinFile, tag))
		}
	}
	for _, rel := range p.releases {
		got, err := r.tagCommit(rel.tag)
		if err != nil {
			return nil, "", "", err
		}
		if got != rel.commit {
			findings = append(findings, fmt.Sprintf("pin: %s says %s is %s, and the tag names %s -- Check B would hold the tree to a commit nobody published.", pinFile, rel.tag, rel.commit, got))
		}
		if err := r.ensureCommit(rel.commit, rel.tag+"'s pinned commit"); err != nil {
			return nil, "", "", err
		}
	}

	s := newShared()
	load := func(name string, fill func(string) error) (*api, error) {
		d := filepath.Join(scratch, name)
		if err := fill(d); err != nil {
			return nil, err
		}
		a, err := s.loadAPI(d)
		if err != nil {
			return nil, cannot("%s: %v", name, err)
		}
		return a, nil
	}
	mainAPI, err := load("main", func(d string) error { return r.materializeCommit(p.main, d) })
	if err != nil {
		return nil, "", "", err
	}
	here, err := load("worktree", r.materializeWorkTree)
	if err != nil {
		return nil, "", "", err
	}

	findings = append(findings, matchCarveouts(parityDiffs(mainAPI, here), sections, carveoutFile)...)

	rep := compatReport{mainPin: p.main, mainFeatures: featureCount(mainAPI), sections: sections}
	for _, rel := range p.releases {
		relAPI, err := load(rel.tag, func(d string) error { return r.materializeCommit(rel.commit, d) })
		if err != nil {
			return nil, "", "", err
		}
		if relAPI.modulePath != here.modulePath {
			findings = append(findings, fmt.Sprintf("compatibility: %s is module %s and this tree is %s -- a release of another module is no baseline for this one.", rel.tag, relAPI.modulePath, here.modulePath))
		}
		findings = append(findings, compatibilityFindings(rel.tag, relAPI, here)...)
		rep.releases = append(rep.releases, releaseReport{tag: rel.tag, commit: rel.commit, features: featureCount(relAPI)})
	}
	return findings, renderCompat(rep), top, nil
}
