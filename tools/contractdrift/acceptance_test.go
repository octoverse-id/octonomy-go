package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// #98's acceptance, end to end: `make contract-check` FAILS on a removed query
// parameter, a dropped schema field, a retyped property and a rerouted method,
// one fixture per case.
//
// This file is this line's own (main.pin marks it `own`). main's tests at
// 5e40964 prove each of those findings in process, and they run here too -- but in
// process, the SDK under test is the one compiled into the test binary, so those
// tests synthesize the client-side half (TestUnsentQueryParameterIsReported deletes a key from an
// observation; TestWrongRouteIsReported rewrites one). That proves the CHECK. It
// does not prove the GATE: that the Makefile target builds the tool against the
// tree in front of it, that the replace in go.mod points at that tree, that the
// binary's exit status survives make, and that a defect in the client's own
// source -- not a synthesized observation -- reaches the report.
//
// So each case here stages a copy of the repository, breaks ONE thing in it --
// the SDK's source for the three client-side cases, the vendored specs for the
// retype -- and runs the real target in the copy. A control run of the unbroken
// copy has to pass first, or every failure below could be a staging fault.
//
// Each run builds the gate, so this is the slowest test in the module (a few
// seconds a case with a warm build cache). It does not skip: `make` missing is a
// failure, since the target IS what is under test.

// acceptanceCase is one fixture: what it breaks, and the finding that names it.
type acceptanceCase struct {
	name    string
	breakIt func(t *testing.T, repo string)
	// want is a substring of the report line the defect must produce. The exit
	// status alone would pass a gate that fails for the wrong reason.
	want string
}

func TestMakeContractCheckFailsOnEachAcceptanceFixture(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Fatal("make is not installed, and `make contract-check` is what this test exercises")
	}

	// The control. A staging fault -- a file the copy lacks, a replace that
	// resolves elsewhere -- fails every case below for a reason none of them is
	// about, so the unbroken copy has to come out clean first.
	if out, err := makeContractCheck(t, stageMakeRepo(t)); err != nil {
		t.Fatalf("the unbroken staged copy failed `make contract-check`, so the cases below would prove nothing:\n%s", out)
	} else if !strings.Contains(out, "No drift.") {
		t.Fatalf("the unbroken staged copy passed without saying `No drift.`:\n%s", out)
	}

	cases := []acceptanceCase{
		{
			// The client stops putting a documented parameter on the wire. Not a
			// synthesized observation: the line that sets it is gone from tags.go.
			name: "a removed query parameter",
			breakIt: func(t *testing.T, repo string) {
				edit(t, filepath.Join(repo, "tags.go"), "func (p *TagListParams) query() url.Values {",
					`q.Set("vocabulary_id", *p.VocabularyID)`, `_ = p.VocabularyID`)
			},
			want: "`get /tags` documents the query parameter `vocabulary_id` and the client did not send it",
		},
		{
			// The model stops decoding a property the schema documents. `json:"-"`
			// rather than a deleted field, so nothing else in the package stops
			// compiling and the failure is the gate's, not the build's.
			name: "a dropped schema field",
			breakIt: func(t *testing.T, repo string) {
				edit(t, filepath.Join(repo, "tags.go"), "type Tag struct {",
					"`json:\"usage_count\"`", "`json:\"-\"`")
			},
			want: "schema `Tag` documents `usage_count` and it does not survive decoding",
		},
		{
			// The contract retypes a property the model still has a field for. The
			// name survives, so a name comparison sees nothing; the stub now sends a
			// string into an int, and the decode is what fails.
			name: "a retyped property",
			breakIt: func(t *testing.T, repo string) {
				for _, spec := range []string{"openapi.yaml", "openapi-v2.yaml"} {
					edit(t, filepath.Join(repo, "docs", spec), "\n    Tag:\n",
						"        usage_count:\n          type: integer\n",
						"        usage_count:\n          type: string\n")
				}
			},
			want: "could not handle a response built from the vendored schema",
		},
		{
			// The method the inventory names requests a different route.
			name: "a rerouted method",
			breakIt: func(t *testing.T, repo string) {
				edit(t, filepath.Join(repo, "tags.go"), "func (s *TagService) Get(",
					`"/tags/"+url.PathEscape(id)`, `"/tag/"+url.PathEscape(id)`)
			},
			want: "`TagService.Get` requests `GET /tag/{tag_id}`",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := stageMakeRepo(t)
			tc.breakIt(t, repo)
			out, err := makeContractCheck(t, repo)
			if err == nil {
				t.Fatalf("`make contract-check` exited 0 on %s:\n%s", tc.name, out)
			}
			if !strings.Contains(out, tc.want) {
				t.Fatalf("`make contract-check` failed on %s without the finding that names it (%q):\n%s", tc.name, tc.want, out)
			}
		})
	}
}

// makeContractCheck runs the real target in a staged repository.
func makeContractCheck(t *testing.T, repo string) (string, error) {
	t.Helper()
	cmd := exec.Command("make", "--no-print-directory", "-C", repo, "contract-check")
	// The staged copy is not a git checkout and has no go.work; GOFLAGS from the
	// caller's environment would change how the copy builds, so it is cleared.
	cmd.Env = append(os.Environ(), "GOFLAGS=", "GOWORK=off")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// stageMakeRepo copies what `make contract-check` reads into a temp directory:
// the SDK's sources and go.mod, the Makefile, the four documents the gate loads,
// and the gate's own module. It is stageRepo plus the parts a BUILD needs, which
// stageRepo leaves out because the in-process tests compile the SDK in rather than
// building it.
func stageMakeRepo(t *testing.T) string {
	t.Helper()
	dir := stageRepo(t)
	copyFile(t, filepath.Join(repoRoot, "go.mod"), filepath.Join(dir, "go.mod"))
	copyFile(t, filepath.Join(repoRoot, "Makefile"), filepath.Join(dir, "Makefile"))

	gate := filepath.Join(dir, "tools", "contractdrift")
	if err := os.MkdirAll(gate, 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	copied := 0
	for _, entry := range entries {
		name := entry.Name()
		switch {
		case entry.IsDir(), strings.HasSuffix(name, "_test.go"):
			continue
		case strings.HasSuffix(name, ".go"), name == "go.mod", name == "go.sum":
			copyFile(t, name, filepath.Join(gate, name))
			copied++
		}
	}
	if copied < 3 {
		t.Fatalf("staged only %d of the gate's own files", copied)
	}
	return dir
}
