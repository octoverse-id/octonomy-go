package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// --- the pin file ------------------------------------------------------------------

// pins is tools/compatdiff/baselines.pin: the commit of main Check A compares
// with, and each published release of this line Check B holds the tree to.
type pins struct {
	main     string
	releases []releasePin
}

type releasePin struct {
	tag    string
	commit string
}

var (
	shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
	// Only v0 and v1 tags exist on an unsuffixed module path, and this line
	// never publishes a v0.
	releaseTagPattern = regexp.MustCompile(`^v1\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$`)
)

// parsePins reads the pin file strictly, as scripts/contract-identity.sh reads
// its own: one `main` line, at least one `release` line, full commit ids, and
// nothing it does not understand.
func parsePins(file string) (*pins, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	p := &pins{}
	tags := map[string]bool{}
	for i, line := range strings.Split(string(raw), "\n") {
		n := i + 1
		line = strings.TrimSuffix(line, "\r") // a CRLF checkout
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		bad := func(format string, args ...any) error {
			return fmt.Errorf("%s:%d: %s", file, n, fmt.Sprintf(format, args...))
		}
		fields := strings.Split(line, " ")
		switch {
		case fields[0] == "main" && len(fields) == 2:
			if p.main != "" {
				return nil, bad("a second main line")
			}
			if !shaPattern.MatchString(fields[1]) {
				return nil, bad("main must name a full forty-hex-digit commit id, got %q", fields[1])
			}
			p.main = fields[1]
		case fields[0] == "release" && len(fields) == 3:
			if !releaseTagPattern.MatchString(fields[1]) {
				return nil, bad("%q is not a release tag of this line (v1.x.y)", fields[1])
			}
			if tags[fields[1]] {
				return nil, bad("release %s is listed twice", fields[1])
			}
			tags[fields[1]] = true
			if !shaPattern.MatchString(fields[2]) {
				return nil, bad("release %s must name a full forty-hex-digit commit id, got %q", fields[1], fields[2])
			}
			p.releases = append(p.releases, releasePin{tag: fields[1], commit: fields[2]})
		default:
			return nil, bad("expected `main <commit>` or `release <tag> <commit>`, separated by single spaces, got %q", line)
		}
	}
	if p.main == "" {
		return nil, fmt.Errorf("%s has no main line", file)
	}
	if len(p.releases) == 0 {
		return nil, fmt.Errorf("%s has no release line -- Check B would hold this line to nothing", file)
	}
	return p, nil
}

// --- git ------------------------------------------------------------------------------

// errCannot marks a failure that means the check could not be MADE, as opposed
// to one that found something. It exits 2, never 1: a check that could not read
// its inputs must never be mistaken for one that found nothing wrong, nor for
// one that found a divergence.
var errCannot = errors.New("cannot check")

func cannot(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errCannot, fmt.Sprintf(format, args...))
}

type repo struct {
	dir        string // the work tree's top level
	remote     string
	mainBranch string
	env        []string
}

// openRepo finds the work tree containing dir. Git runs with the repository's
// local environment variables (GIT_DIR, GIT_INDEX_FILE, ...) removed from its
// environment, as `git rev-parse --local-env-vars` lists them: a hook or a
// parent git that set them would otherwise redirect every read below to
// another repository or index.
func openRepo(dir, remote, mainBranch string) (*repo, error) {
	r := &repo{remote: remote, mainBranch: mainBranch, env: os.Environ()}
	out, err := r.git(dir, "rev-parse", "--local-env-vars")
	if err != nil {
		return nil, cannot("git is not usable here: %v", err)
	}
	drop := map[string]bool{}
	for _, name := range strings.Fields(out) {
		drop[name] = true
	}
	// GIT_NO_REPLACE_OBJECTS too: a refs/replace/ entry would make every read
	// below return other content under the very commit id the pins validate.
	drop["GIT_NO_REPLACE_OBJECTS"] = true
	env := []string{"GIT_NO_REPLACE_OBJECTS=1"}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !drop[name] {
			env = append(env, kv)
		}
	}
	r.env = env
	top, err := r.git(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, cannot("%s is not inside a git work tree", dir)
	}
	r.dir = top
	shallow, err := r.git(top, "rev-parse", "--is-shallow-repository")
	if err != nil {
		return nil, cannot("%v", err)
	}
	if shallow == "true" {
		return nil, cannot("this clone is shallow, so whether the pin is on %s/%s cannot be decided -- fetch full history (actions/checkout: fetch-depth: 0)", remote, mainBranch)
	}
	return r, nil
}

func (r *repo) git(dir string, args ...string) (string, error) {
	out, err := r.gitRaw(dir, args...)
	return strings.TrimSpace(out), err
}

// gitRaw is git's output untrimmed, for the NUL-separated listings: trimming
// one would eat a leading space from its first path.
func (r *repo) gitRaw(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = r.env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

func (r *repo) run(args ...string) (string, error) { return r.git(r.dir, args...) }

func (r *repo) hasCommit(sha string) bool {
	_, err := r.run("cat-file", "-e", sha+"^{commit}")
	return err == nil
}

// ensureCommit makes sha available locally, fetching it from the remote only
// when it is missing, so a clone that already has it runs offline.
func (r *repo) ensureCommit(sha, what string) error {
	if r.hasCommit(sha) {
		return nil
	}
	if _, err := r.run("fetch", "--no-tags", "--quiet", r.remote, sha); err != nil {
		return cannot("%s %s is not in this clone and could not be fetched from %s: %v", what, sha, r.remote, err)
	}
	if !r.hasCommit(sha) {
		return cannot("fetched %s %s from %s and it is not a commit", what, sha, r.remote)
	}
	return nil
}

// pinOnMain reports whether sha is one of main's commits. Only ancestry is
// read from the branch, and ancestry is monotonic: once the pin is on main it
// stays there, so main moving on cannot turn an unchanged tree here red.
//
// It trusts the LOCAL ref when that already answers yes, as tagCommit trusts a
// local tag, so a clone whose refs were rewritten by hand can pass here. That is
// a limitation of a laptop, not of the gate: CI checks out fresh, from the
// remote, on every run.
func (r *repo) pinOnMain(sha string) (bool, error) {
	ref := "refs/remotes/" + r.remote + "/" + r.mainBranch
	isAncestor := func() bool {
		if _, err := r.run("rev-parse", "-q", "--verify", ref+"^{commit}"); err != nil {
			return false
		}
		_, err := r.run("merge-base", "--is-ancestor", sha, ref)
		return err == nil
	}
	if isAncestor() {
		return true, nil
	}
	if _, err := r.run("fetch", "--no-tags", "--quiet", r.remote, "+refs/heads/"+r.mainBranch+":"+ref); err != nil {
		return false, cannot("could not fetch %s from %s to decide whether the pin is on it: %v", r.mainBranch, r.remote, err)
	}
	return isAncestor(), nil
}

// tagCommit is the commit a release tag names, fetching the tag when it is not
// already local.
func (r *repo) tagCommit(tag string) (string, error) {
	ref := "refs/tags/" + tag
	resolve := func() (string, error) { return r.run("rev-parse", "-q", "--verify", ref+"^{commit}") }
	if sha, err := resolve(); err == nil {
		return sha, nil
	}
	if _, err := r.run("fetch", "--no-tags", "--quiet", r.remote, "+"+ref+":"+ref); err != nil {
		return "", cannot("release tag %s is not in this clone and could not be fetched from %s: %v", tag, r.remote, err)
	}
	sha, err := resolve()
	if err != nil {
		return "", cannot("fetched %s from %s and it names no commit", tag, r.remote)
	}
	return sha, nil
}

// releaseTags lists this clone's tags that name a release of this line --
// every v1.x tag, prereleases included, since a published one has callers.
func (r *repo) releaseTags() ([]string, error) {
	out, err := r.run("for-each-ref", "--format=%(refname:strip=2)", "refs/tags/v1.*")
	if err != nil {
		return nil, cannot("%v", err)
	}
	var tags []string
	for _, t := range strings.Fields(out) {
		if releaseTagPattern.MatchString(t) {
			tags = append(tags, t)
		}
	}
	return tags, nil
}

// wanted is what a tree is materialized with: the Go files and go.mod files,
// which are everything loadAPI reads.
func wanted(p string) bool {
	return strings.HasSuffix(p, ".go") || path.Base(p) == "go.mod"
}

// safeRel refuses a path that would land outside the destination.
func safeRel(p string) error {
	if p == "" || path.IsAbs(p) || path.Clean(p) != p || p == ".." || strings.HasPrefix(p, "../") {
		return cannot("git listed the unusable path %q", p)
	}
	return nil
}

// materializeCommit writes the Go files and go.mod files of a commit's tree
// into dst. It reads the tree object by object (ls-tree, then cat-file) rather
// than through `git archive`, which honours export-ignore and export-subst
// attributes and so can hand back something other than the commit's content.
func (r *repo) materializeCommit(sha, dst string) error {
	listing, err := r.gitRaw(r.dir, "ls-tree", "-r", "-z", "--full-tree", sha)
	if err != nil {
		return cannot("%v", err)
	}
	type blob struct{ id, path string }
	var blobs []blob
	for _, entry := range strings.Split(listing, "\x00") {
		if entry == "" {
			continue
		}
		meta, p, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 {
			return cannot("unexpected ls-tree entry %q", entry)
		}
		if !wanted(p) {
			continue
		}
		if err := safeRel(p); err != nil {
			return err
		}
		mode, kind, id := fields[0], fields[1], fields[2]
		if kind != "blob" {
			continue // a submodule; nothing of this module's
		}
		if mode != "100644" && mode != "100755" {
			return cannot("%s:%s is a %s, not a regular file, and this tool does not follow it", short(sha), p, mode)
		}
		blobs = append(blobs, blob{id, p})
	}
	if len(blobs) == 0 {
		return cannot("commit %s has no Go files", sha)
	}
	cmd := exec.Command("git", "-C", r.dir, "cat-file", "--batch")
	cmd.Env = r.env
	var ids bytes.Buffer
	for _, b := range blobs {
		ids.WriteString(b.id + "\n")
	}
	cmd.Stdin = &ids
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return cannot("git cat-file --batch: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	rd := bufio.NewReader(bytes.NewReader(out))
	for _, b := range blobs {
		header, err := rd.ReadString('\n')
		if err != nil {
			return cannot("reading %s: %v", b.path, err)
		}
		f := strings.Fields(header)
		if len(f) != 3 || f[0] != b.id || f[1] != "blob" {
			return cannot("unexpected cat-file header %q for %s", strings.TrimSpace(header), b.path)
		}
		size, err := strconv.Atoi(f[2])
		if err != nil {
			return cannot("unexpected cat-file size in %q", strings.TrimSpace(header))
		}
		content := make([]byte, size)
		if _, err := io.ReadFull(rd, content); err != nil {
			return cannot("reading %s: %v", b.path, err)
		}
		if _, err := rd.ReadByte(); err != nil { // the newline after each object
			return cannot("reading %s: %v", b.path, err)
		}
		if err := writeFile(dst, b.path, content); err != nil {
			return err
		}
	}
	return nil
}

// materializeWorkTree writes the work tree's Go files and go.mod files into
// dst: what git tracks, plus untracked files it would not ignore -- a new file
// is part of the tree under review before it is committed, not after -- and
// never an ignored one, so a stray scratch file cannot change the answer.
func (r *repo) materializeWorkTree(dst string) error {
	listing, err := r.gitRaw(r.dir, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return cannot("%v", err)
	}
	seen := map[string]bool{}
	for _, p := range strings.Split(listing, "\x00") {
		if p == "" || seen[p] || !wanted(p) {
			continue
		}
		seen[p] = true
		if err := safeRel(p); err != nil {
			return err
		}
		src := filepath.Join(r.dir, filepath.FromSlash(p))
		info, err := os.Lstat(src)
		if errors.Is(err, os.ErrNotExist) {
			continue // tracked and deleted in the work tree: not part of it
		}
		if err != nil {
			return cannot("%v", err)
		}
		if !info.Mode().IsRegular() {
			return cannot("%s is not a regular file, and this tool does not follow it", p)
		}
		content, err := os.ReadFile(src)
		if err != nil {
			return cannot("%v", err)
		}
		if err := writeFile(dst, p, content); err != nil {
			return err
		}
	}
	return nil
}

func writeFile(dst, rel string, content []byte) error {
	target := filepath.Join(dst, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return cannot("%v", err)
	}
	if err := os.WriteFile(target, content, 0o644); err != nil {
		return cannot("%v", err)
	}
	return nil
}
