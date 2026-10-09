package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The command end to end, against throwaway repositories laid out like this
// one: an origin whose main branch is the modern module and whose compat
// branch is this line, with a published release tag on it.

// hermeticGit points git at an empty configuration for the rest of the test,
// so neither the fixture nor the command under test reads -- or, worse,
// writes -- the caller's own git config.
func hermeticGit(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	for k, v := range map[string]string{
		"HOME":                home,
		"XDG_CONFIG_HOME":     home,
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_CONFIG_GLOBAL":   filepath.Join(home, "gitconfig"),
		"GIT_AUTHOR_NAME":     "fixture",
		"GIT_AUTHOR_EMAIL":    "fixture@example.com",
		"GIT_COMMITTER_NAME":  "fixture",
		"GIT_COMMITTER_EMAIL": "fixture@example.com",
		"GITHUB_ACTIONS":      "",
		"COMPATDIFF_REMOTE":   "",
		"COMPATDIFF_MAIN":     "",
	} {
		t.Setenv(k, v)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func commitAll(t *testing.T, dir, msg string) string {
	t.Helper()
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "--allow-empty", "-m", msg)
	return git(t, dir, "rev-parse", "HEAD")
}

type fixtureRepo struct {
	origin  string // file:// URL
	work    string // a full clone, on the compat branch
	mainSHA string
	relSHA  string // what v1.0.0 names
	headSHA string // the compat branch's tip
}

const fixtureCarveouts = `section carveout default
title The default
text Why it differs.
main const Default untyped string = "v2"
compat const Default untyped string = "v1"

section carveout hook
title No hook package
text Policy.
main package hook

section dialect generics
title Generics
text Per type.
main func Each[T any](T)
compat func Each(interface{})
`

func newFixtureRepo(t *testing.T) *fixtureRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	hermeticGit(t)
	root := t.TempDir()
	bare := filepath.Join(root, "origin.git")
	seed := filepath.Join(root, "seed")
	git(t, root, "init", "-q", "--bare", bare)
	git(t, root, "init", "-q", "-b", "main", seed)
	f := &fixtureRepo{origin: "file://" + bare}

	writeTree(t, seed, map[string]string{
		"go.mod":       "module example.com/m/v2\n\ngo 1.24\n",
		"m.go":         "package m\n\nconst Default = \"v2\"\n\nfunc Shared() {}\n\nfunc Each[T any](T) {}\n",
		"hook/hook.go": "package hook\n\nfunc Verify() {}\n",
		// Pins why the tree is read object by object: `git archive` would
		// leave every Go file of this commit out.
		".gitattributes": "*.go export-ignore\n",
	})
	f.mainSHA = commitAll(t, seed, "main")

	git(t, seed, "checkout", "-q", "--orphan", "compat")
	git(t, seed, "rm", "-rq", "--cached", ".")
	for _, p := range []string{"m.go", "hook", ".gitattributes"} {
		if err := os.RemoveAll(filepath.Join(seed, p)); err != nil {
			t.Fatal(err)
		}
	}
	writeTree(t, seed, map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.13\n",
		"m.go":   "package m\n\nconst Default = \"v1\"\n\nfunc Shared() {}\n\nfunc Each(interface{}) {}\n",
	})
	f.relSHA = commitAll(t, seed, "release")
	git(t, seed, "tag", "v1.0.0")
	writeTree(t, seed, map[string]string{
		"tools/compatdiff/baselines.pin": "main " + f.mainSHA + "\nrelease v1.0.0 " + f.relSHA + "\n",
		"tools/compatdiff/carveouts.txt": fixtureCarveouts,
	})
	f.headSHA = commitAll(t, seed, "pins")
	git(t, seed, "push", "-q", f.origin, "main", "compat", "v1.0.0")

	f.work = filepath.Join(root, "work")
	git(t, root, "clone", "-q", "-b", "compat", f.origin, f.work)
	if code, out := runIn(t, f.work, "write"); code != 0 {
		t.Fatalf("write on the clean fixture exited %d:\n%s", code, out)
	}
	commitAll(t, f.work, "COMPAT.md")
	return f
}

func runIn(t *testing.T, dir string, args ...string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := run(append([]string{"-repo", dir}, args...), &out, &out)
	return code, out.String()
}

func (f *fixtureRepo) write(t *testing.T, rel, content string) {
	t.Helper()
	writeTree(t, f.work, map[string]string{rel: content})
}

func expect(t *testing.T, code int, out string, wantCode int, wantIn ...string) {
	t.Helper()
	if code != wantCode {
		t.Errorf("exit %d, want %d:\n%s", code, wantCode, out)
	}
	for _, w := range wantIn {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q:\n%s", w, out)
		}
	}
}

func TestCommandOnAFixtureRepository(t *testing.T) {
	f := newFixtureRepo(t)

	t.Run("clean", func(t *testing.T) {
		code, out := runIn(t, f.work, "check")
		expect(t, code, out, 0, "COMPAT.md is current")
		compat, err := os.ReadFile(filepath.Join(f.work, "COMPAT.md"))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"```text\npackage hook\n```", "(`" + f.relSHA[:12] + "`): 3 features."} {
			if !strings.Contains(string(compat), want) {
				t.Errorf("COMPAT.md lacks %q:\n%s", want, compat)
			}
		}
	})

	t.Run("an untracked file is part of the tree", func(t *testing.T) {
		f.write(t, "extra.go", "package m\n\nfunc Extra() {}\n")
		defer func() { _ = os.Remove(filepath.Join(f.work, "extra.go")) }()
		code, out := runIn(t, f.work, "check")
		expect(t, code, out, 1, "parity: func Extra() -- this line has it and main does not")
	})

	t.Run("an ignored file is not", func(t *testing.T) {
		f.write(t, ".git/info/exclude", "scratch.go\n")
		f.write(t, "scratch.go", "package m\n\nfunc Scratch() {}\n")
		defer func() { _ = os.Remove(filepath.Join(f.work, "scratch.go")) }()
		code, out := runIn(t, f.work, "check")
		expect(t, code, out, 0)
	})

	t.Run("the work tree, not HEAD", func(t *testing.T) {
		// A committed file that would not type-check, deleted in the work tree:
		// read from HEAD it fails the load, read from the work tree it is gone.
		f.write(t, "dup.go", "package m\n\nfunc Shared() {}\n")
		commitAll(t, f.work, "dup")
		if err := os.Remove(filepath.Join(f.work, "dup.go")); err != nil {
			t.Fatal(err)
		}
		code, out := runIn(t, f.work, "check")
		expect(t, code, out, 0)
		git(t, f.work, "reset", "-q", "--hard", "HEAD~1")
	})

	t.Run("write refuses to record a failing comparison", func(t *testing.T) {
		before, _ := os.ReadFile(filepath.Join(f.work, "COMPAT.md"))
		f.write(t, "extra.go", "package m\n\nfunc Extra() {}\n")
		defer func() { _ = os.Remove(filepath.Join(f.work, "extra.go")) }()
		code, out := runIn(t, f.work, "write")
		expect(t, code, out, 1, "func Extra()")
		after, _ := os.ReadFile(filepath.Join(f.work, "COMPAT.md"))
		if !bytes.Equal(before, after) {
			t.Error("write changed COMPAT.md although a check failed")
		}
	})

	t.Run("a stale COMPAT.md", func(t *testing.T) {
		p := filepath.Join(f.work, "COMPAT.md")
		before, _ := os.ReadFile(p)
		defer func() { _ = os.WriteFile(p, before, 0o644) }()
		if err := os.WriteFile(p, append(before, "hand edit\n"...), 0o644); err != nil {
			t.Fatal(err)
		}
		code, out := runIn(t, f.work, "check")
		expect(t, code, out, 1, "COMPAT.md: is not what the checks produce")
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
		code, out = runIn(t, f.work, "check")
		expect(t, code, out, 1, "COMPAT.md: does not exist")
	})

	t.Run("a stale carve-out line", func(t *testing.T) {
		p := "tools/compatdiff/carveouts.txt"
		defer f.write(t, p, fixtureCarveouts)
		f.write(t, p, fixtureCarveouts+"compat func Removed()\n")
		code, out := runIn(t, f.work, "check")
		expect(t, code, out, 1, "lists `compat func Removed()` under generics, and there is no such difference")
	})

	t.Run("a break of the release", func(t *testing.T) {
		p := filepath.Join(f.work, "m.go")
		before, _ := os.ReadFile(p)
		defer func() { _ = os.WriteFile(p, before, 0o644) }()
		changed := strings.Replace(string(before), "func Shared() {}", "func Shared(int) {}", 1)
		if err := os.WriteFile(p, []byte(changed), 0o644); err != nil {
			t.Fatal(err)
		}
		code, out := runIn(t, f.work, "check")
		// It is a parity difference too; the compatibility finding is what
		// matters, since no carve-out can excuse it.
		expect(t, code, out, 1, "compatibility: v1.0.0 exports `func Shared()`, and this tree does not")
	})

	t.Run("a pin off main", func(t *testing.T) {
		p := "tools/compatdiff/baselines.pin"
		good := "main " + f.mainSHA + "\nrelease v1.0.0 " + f.relSHA + "\n"
		defer f.write(t, p, good)
		f.write(t, p, "main "+f.relSHA+"\nrelease v1.0.0 "+f.relSHA+"\n")
		code, out := runIn(t, f.work, "check")
		expect(t, code, out, 1, "names "+f.relSHA+" for main, and it is not on origin/main")
	})

	t.Run("a release pin the tag does not name", func(t *testing.T) {
		p := "tools/compatdiff/baselines.pin"
		good := "main " + f.mainSHA + "\nrelease v1.0.0 " + f.relSHA + "\n"
		defer f.write(t, p, good)
		f.write(t, p, "main "+f.mainSHA+"\nrelease v1.0.0 "+f.headSHA+"\n")
		code, out := runIn(t, f.work, "check")
		expect(t, code, out, 1, "says v1.0.0 is "+f.headSHA+", and the tag names "+f.relSHA)
	})

	t.Run("a release of another module", func(t *testing.T) {
		// Built with plumbing, so the work tree never leaves the compat branch:
		// a commit whose go.mod names another module, tagged as a release.
		blob := func(content string) string {
			cmd := exec.Command("git", "-C", f.work, "hash-object", "-w", "--stdin")
			cmd.Stdin = strings.NewReader(content)
			out, err := cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			return strings.TrimSpace(string(out))
		}
		entries := "100644 blob " + blob("module example.com/other\n\ngo 1.13\n") + "\tgo.mod\n" +
			"100644 blob " + blob("package m\n\nconst Default = \"v1\"\n\nfunc Shared() {}\n\nfunc Each(interface{}) {}\n") + "\tm.go\n"
		mk := exec.Command("git", "-C", f.work, "mktree")
		mk.Stdin = strings.NewReader(entries)
		tree, err := mk.Output()
		if err != nil {
			t.Fatal(err)
		}
		other := git(t, f.work, "commit-tree", strings.TrimSpace(string(tree)), "-m", "another module")
		git(t, f.work, "tag", "v1.0.1", other)
		defer git(t, f.work, "tag", "-d", "v1.0.1")
		p := "tools/compatdiff/baselines.pin"
		good := "main " + f.mainSHA + "\nrelease v1.0.0 " + f.relSHA + "\n"
		defer f.write(t, p, good)
		f.write(t, p, good+"release v1.0.1 "+other+"\n")
		code, out := runIn(t, f.work, "check")
		expect(t, code, out, 1, "v1.0.1 is module example.com/other and this tree is example.com/m")
	})

	t.Run("a missing tag and main ref are fetched", func(t *testing.T) {
		git(t, f.work, "tag", "-d", "v1.0.0")
		git(t, f.work, "update-ref", "-d", "refs/remotes/origin/main")
		code, out := runIn(t, f.work, "check")
		expect(t, code, out, 0)
		git(t, f.work, "rev-parse", "--verify", "refs/tags/v1.0.0")
	})

	t.Run("a hook's GIT_DIR does not redirect the reads", func(t *testing.T) {
		t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "elsewhere.git"))
		t.Setenv("GIT_INDEX_FILE", filepath.Join(t.TempDir(), "index"))
		code, out := runIn(t, f.work, "check")
		expect(t, code, out, 0)
	})

	t.Run("an unreadable carve-out file cannot be checked", func(t *testing.T) {
		p := "tools/compatdiff/carveouts.txt"
		defer f.write(t, p, fixtureCarveouts)
		f.write(t, p, "mian x\n")
		code, out := runIn(t, f.work, "check")
		expect(t, code, out, 2, "could not check")
	})

	t.Run("an unreadable pin file cannot be checked", func(t *testing.T) {
		p := "tools/compatdiff/baselines.pin"
		good := "main " + f.mainSHA + "\nrelease v1.0.0 " + f.relSHA + "\n"
		defer f.write(t, p, good)
		f.write(t, p, "main 5e40964\n")
		code, out := runIn(t, f.work, "check")
		expect(t, code, out, 2, "forty-hex")
	})

	t.Run("a pin nobody has cannot be checked", func(t *testing.T) {
		p := "tools/compatdiff/baselines.pin"
		good := "main " + f.mainSHA + "\nrelease v1.0.0 " + f.relSHA + "\n"
		defer f.write(t, p, good)
		f.write(t, p, "main "+strings.Repeat("ab", 20)+"\nrelease v1.0.0 "+f.relSHA+"\n")
		code, out := runIn(t, f.work, "check")
		expect(t, code, out, 2, "could not be fetched")
	})
}

func TestCommandRefusesWhatItCannotDecide(t *testing.T) {
	f := newFixtureRepo(t)

	shallow := filepath.Join(t.TempDir(), "shallow")
	git(t, filepath.Dir(shallow), "clone", "-q", "--depth", "1", "-b", "compat", f.origin, shallow)
	code, out := runIn(t, shallow, "check")
	expect(t, code, out, 2, "shallow")

	code, out = runIn(t, t.TempDir(), "check")
	expect(t, code, out, 2, "not inside a git work tree")

	for _, args := range [][]string{{}, {"verify"}, {"check", "extra"}, {"-nope", "check"}} {
		if code, out := runIn(t, f.work, args...); code != 2 {
			t.Errorf("args %q: exit %d, want 2:\n%s", args, code, out)
		}
	}
}
