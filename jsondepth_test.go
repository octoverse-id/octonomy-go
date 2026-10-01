package octonomy

// Tests for the JSON nesting guards (jsondepth.go). There is nothing in main to
// port: its toolchain's encoding/json bounds both directions itself.
//
// Two kinds of test, split by what an unguarded run would do:
//
//   - In-process, for inputs that are only just over the ceiling. Unguarded, Go
//     1.13 would handle these fine -- they are refused by policy, not to save
//     the process -- so a regression fails an assertion rather than the binary.
//   - In a CHILD PROCESS, for the inputs the guards exist for: a cycle, and a
//     body millions of levels deep. Unguarded, Go 1.13 hangs on the first and
//     dies of a fatal stack overflow on the second, and neither is a panic a
//     test can recover from -- it would take the whole test binary down, or
//     wedge it until the suite's timeout. Run in a child, it is one failed test
//     that says which guard is missing.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// nestedArrays returns depth levels of [ ... ] around inner.
func nestedArrays(depth int, inner string) string {
	return strings.Repeat("[", depth) + inner + strings.Repeat("]", depth)
}

// nestedMetadata returns a Metadata nested depth maps deep.
func nestedMetadata(depth int) Metadata {
	m := Metadata{"leaf": true}
	for i := 1; i < depth; i++ {
		m = Metadata{"k": m}
	}
	return m
}

// --- checkJSONDepth ---------------------------------------------------------

func TestCheckJSONDepth(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		wantErr bool
	}{
		{"flat object", `{"a":1,"b":[1,2,3]}`, false},
		{"exactly at the ceiling", nestedArrays(maxJSONDepth, ""), false},
		{"one level over", nestedArrays(maxJSONDepth+1, ""), true},
		{"objects count like arrays", strings.Repeat(`{"k":`, maxJSONDepth+1) + "1" + strings.Repeat("}", maxJSONDepth+1), true},
		{"mixed nesting at the ceiling", strings.Repeat(`[{"k":`, maxJSONDepth/2) + "1" + strings.Repeat("}]", maxJSONDepth/2), false},
		// A bracket inside a string is content, not structure.
		{"brackets inside a string", `{"a":"` + strings.Repeat("[", 2*maxJSONDepth) + `"}`, false},
		// An escaped quote does not end the string, so what follows it is still
		// content...
		{"escaped quote inside a string", `{"a":"\"` + strings.Repeat("[", 2*maxJSONDepth) + `"}`, false},
		// ...but an escaped BACKSLASH does not escape the quote after it, so the
		// string ends there and the brackets after it are structure.
		{"escaped backslash ends the string", `["\\"` + strings.Repeat(",[", maxJSONDepth) + strings.Repeat("]", maxJSONDepth) + `]`, true},
		// Siblings do not add up: depth is how far in, not how many.
		{"many shallow siblings", "[" + strings.Repeat("[[]],", 3*maxJSONDepth) + "[]]", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			err := checkJSONDepth([]byte(tt.data))
			if tt.wantErr != (err != nil) {
				t.Fatalf("checkJSONDepth = %v, want error: %v", err, tt.wantErr)
			}
			if err != nil && err != errJSONTooDeep {
				t.Errorf("err = %v, want errJSONTooDeep", err)
			}
		})
	}
}

// The guard's message must not be the standard library's, or a test run on a
// modern toolchain could not tell the guard refusing from the toolchain refusing
// -- and would pass with the guard deleted.
func TestErrJSONTooDeep_IsNotTheStandardLibrarysMessage(t *testing.T) {
	if strings.Contains(errJSONTooDeep.Error(), "exceeded max depth") {
		t.Errorf("errJSONTooDeep = %q reuses the standard library's wording", errJSONTooDeep)
	}
}

// A response just over the ceiling is refused at every decode site: the single
// resource, the list, an error envelope's details, and a health body.
func TestDecode_RefusesAResponseOverTheCeiling(t *testing.T) {
	deep := nestedArrays(maxJSONDepth+1, "1")

	t.Run("single resource", func(t *testing.T) {
		c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			writeRaw(w, http.StatusOK, `{"data":{"id":"t1","metadata":{"k":`+deep+`}}}`)
		})
		defer cleanup()
		_, err := c.Tags.Get(context.Background(), "t1")
		if !errors.Is(err, errJSONTooDeep) {
			t.Fatalf("err = %v, want errJSONTooDeep", err)
		}
	})

	t.Run("list", func(t *testing.T) {
		c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			writeRaw(w, http.StatusOK, `{"data":[{"id":"t1","metadata":{"k":`+deep+`}}],"pagination":{"limit":50}}`)
		})
		defer cleanup()
		_, err := c.Vocabularies.List(context.Background(), nil)
		if !errors.Is(err, errJSONTooDeep) {
			t.Fatalf("err = %v, want errJSONTooDeep", err)
		}
	})

	// A non-2xx is still an *APIError -- it cannot be decoded, so it has no
	// envelope, so it is CodeUnexpectedStatus, exactly as a body the /v2
	// module's decoder refused would be.
	t.Run("error envelope", func(t *testing.T) {
		c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			writeRaw(w, http.StatusBadRequest, `{"error":{"code":"validation_error","details":{"k":`+deep+`}}}`)
		})
		defer cleanup()
		_, err := c.Tags.Get(context.Background(), "t1")
		apiErr, ok := AsAPIError(err)
		if !ok {
			t.Fatalf("a non-2xx must stay an *APIError: %v", err)
		}
		if apiErr.Code != CodeUnexpectedStatus || IsValidation(err) {
			t.Errorf("Code = %q, want %q: the envelope was never decoded, so its code was never established", apiErr.Code, CodeUnexpectedStatus)
		}
	})

	t.Run("health body", func(t *testing.T) {
		hc, cleanup := newTestHealthClient(t, func(w http.ResponseWriter, _ *http.Request) {
			writeRaw(w, http.StatusOK, `{"status":"ok","extra":`+deep+`}`)
		})
		defer cleanup()
		_, err := hc.Health.Live(context.Background())
		if !errors.Is(err, errJSONTooDeep) {
			t.Fatalf("err = %v, want errJSONTooDeep", err)
		}
	})
}

// The converse: a deep but legal response decodes. The ceiling is generous on
// purpose, and an off-by-one that refused real payloads would be a regression.
func TestDecode_AcceptsAResponseWithinTheCeiling(t *testing.T) {
	// The envelope and the Tag object are two levels; the metadata object one
	// more; the array fills the rest, so the body is exactly at the ceiling.
	inner := nestedArrays(maxJSONDepth-3, "1")
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeRaw(w, http.StatusOK, `{"data":{"id":"t1","metadata":{"k":`+inner+`}}}`)
	})
	defer cleanup()

	tag, err := c.Tags.Get(context.Background(), "t1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if tag.ID != "t1" || tag.Metadata["k"] == nil {
		t.Errorf("unexpected tag: id=%q metadata[k] nil=%v", tag.ID, tag.Metadata["k"] == nil)
	}
}

// --- checkBodyDepth ---------------------------------------------------------

func TestCheckBodyDepth(t *testing.T) {
	cyclic := Metadata{}
	cyclic["self"] = cyclic

	a, b := Metadata{}, Metadata{}
	a["b"], b["a"] = b, a

	selfPointer := new(interface{})
	*selfPointer = selfPointer

	type hidden struct {
		Name string
		next *hidden
	}
	loop := &hidden{Name: "loop"}
	loop.next = loop

	tests := []struct {
		name    string
		body    interface{}
		wantErr bool
	}{
		{"nil", nil, false},
		{"a create body", TagCreate{Name: "N", Slug: "n", Type: "t", Description: String("d"), Metadata: Metadata{"k": []interface{}{1, "two"}}}, false},
		{"deep but legal metadata", TagCreate{Metadata: nestedMetadata(maxJSONDepth - 10)}, false},
		{"metadata over the ceiling", TagCreate{Metadata: nestedMetadata(maxJSONDepth + 1)}, true},
		{"metadata that contains itself", TagCreate{Metadata: cyclic}, true},
		{"two maps containing each other", VocabularyCreate{Metadata: a}, true},
		{"an update body, through its value", TagUpdate{Metadata: cyclic}, true},
		{"a pointer to a body", &TagCreate{Metadata: cyclic}, true},
		{"a cycle through no container at all", TagCreate{Metadata: Metadata{"p": selfPointer}}, true},
		{"a large byte slice is one string", TagCreate{Metadata: Metadata{"b": make([]byte, 4*maxJSONDepth)}}, false},
		// encoding/json skips an unexported field, so the walk does too: a cycle
		// there is never encoded and is no reason to refuse the request.
		{"a cycle behind an unexported field", TagCreate{Metadata: Metadata{"h": loop}}, false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			err := checkBodyDepth(tt.body)
			if tt.wantErr != (err != nil) {
				t.Fatalf("checkBodyDepth = %v, want error: %v", err, tt.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "nests deeper than") {
				t.Errorf("error should say what was refused: %v", err)
			}
		})
	}
}

// The request guard runs at the chokepoint, before anything is sent.
func TestDoRaw_RefusesARequestOverTheCeilingBeforeSending(t *testing.T) {
	c, cleanup := newUnreachableClient(t, "")
	defer cleanup()
	_, err := c.Tags.Create(context.Background(), TagCreate{Name: "N", Slug: "n", Type: "t", Metadata: nestedMetadata(maxJSONDepth + 1)})
	if err == nil {
		t.Fatal("expected a client-side error")
	}
	if !strings.Contains(err.Error(), "octonomy: encode request body") {
		t.Errorf("error should name the stage: %v", err)
	}
}

// --- the cases only a child process can observe ------------------------------

const (
	depthChildEnv = "OCTONOMY_JSONDEPTH_CHILD"
	depthChildOK  = "CHILD-OK"
)

// runDepthChild re-runs the calling test in a child process with depthChildEnv
// set to scenario, and fails unless the child reports depthChildOK in time.
//
// The deadline is what turns a hang into a failure, and the exit status is what
// turns a fatal stack overflow into one: both are what an unguarded Go 1.13
// does with these inputs, and neither can be observed from inside the process
// it happens to.
func runDepthChild(t *testing.T, scenario string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+t.Name()+"$")
	child.Env = append(os.Environ(), depthChildEnv+"="+scenario)
	output, err := child.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("child %q did not finish in 90s: the guard is missing and the process hung\n%s", scenario, tail(output))
	}
	if err != nil {
		t.Fatalf("child %q died (%v): the guard is missing and the process could not survive the input\n%s", scenario, err, tail(output))
	}
	if !bytes.Contains(output, []byte(depthChildOK)) {
		t.Fatalf("child %q did not report the refusal:\n%s", scenario, tail(output))
	}
}

// tail keeps a fatal stack overflow's report readable: the first lines name the
// error, and the rest is a frame repeated a million times.
func tail(output []byte) string {
	const keep = 2048
	if len(output) <= keep {
		return string(output)
	}
	return string(output[:keep]) + fmt.Sprintf("\n... (%d more bytes)", len(output)-keep)
}

// childReport is what a child prints: the marker when the guard refused as
// expected, a reason otherwise.
func childReport(err error, wantIn string, reached bool) {
	switch {
	case reached:
		fmt.Println("child: the request reached the server; the guard must refuse before sending")
	case err == nil:
		fmt.Println("child: no error; the input was accepted")
	case !strings.Contains(err.Error(), wantIn):
		fmt.Printf("child: error %q does not come from the guard (want it to contain %q)\n", err, wantIn)
	default:
		fmt.Println(depthChildOK)
	}
}

// Unguarded, Go 1.13's json.Marshal recurses through a self-containing map
// without end -- probed on go1.13.15, the process hangs.
func TestCyclicMetadataIsRefusedRatherThanHanging(t *testing.T) {
	scenario := os.Getenv(depthChildEnv)
	if scenario == "" {
		for _, s := range []string{"tag-create", "tag-update", "vocabulary-create", "vocabulary-update"} {
			s := s
			t.Run(s, func(t *testing.T) { runDepthChild(t, s) })
		}
		return
	}
	// In the child: -test.run selected this test alone, and the scenario
	// arrives by environment rather than as a subtest.

	reached := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		writeData(t, w, http.StatusOK, Tag{ID: "t1"})
	}))
	defer srv.Close()
	c, err := New(Config{BaseURL: srv.URL, Token: "t", TenantID: "acme"})
	if err != nil {
		fmt.Printf("child: New: %v\n", err)
		return
	}

	m := Metadata{"name": "loop"}
	m["self"] = m
	ctx := context.Background()
	switch scenario {
	case "tag-create":
		_, err = c.Tags.Create(ctx, TagCreate{Name: "N", Slug: "n", Type: "t", Metadata: m})
	case "tag-update":
		_, err = c.Tags.Update(ctx, "t1", TagUpdate{Metadata: m})
	case "vocabulary-create":
		_, err = c.Vocabularies.Create(ctx, VocabularyCreate{Name: "N", Slug: "n", Metadata: m})
	case "vocabulary-update":
		_, err = c.Vocabularies.Update(ctx, "v1", VocabularyUpdate{Metadata: m})
	default:
		fmt.Printf("child: unknown scenario %q\n", scenario)
		return
	}
	childReport(err, "nests deeper than", reached)
}

// Unguarded, Go 1.13's json.Unmarshal recurses once per level into whatever
// the server sent -- probed on go1.13.15, 200,000 levels decode with a nil
// error, and enough levels exhaust the stack, which is fatal. Five million
// levels of [ is 10 MB, under the 32 MiB read ceiling, so the ceiling does not
// stop it.
func TestOverDeepResponseIsRefusedRatherThanFatal(t *testing.T) {
	scenario := os.Getenv(depthChildEnv)
	if scenario == "" {
		for _, s := range []string{"data", "list", "error-details", "health"} {
			s := s
			t.Run(s, func(t *testing.T) { runDepthChild(t, s) })
		}
		return
	}
	// In the child: -test.run selected this test alone, and the scenario
	// arrives by environment rather than as a subtest.

	deep := nestedArrays(5000000, "1")
	body := map[string]string{
		"data":          `{"data":{"id":"t1","metadata":{"k":` + deep + `}}}`,
		"list":          `{"data":[{"id":"t1","metadata":{"k":` + deep + `}}],"pagination":{"limit":50}}`,
		"error-details": `{"error":{"code":"validation_error","details":{"k":` + deep + `}}}`,
		"health":        `{"status":"ok","extra":` + deep + `}`,
	}[scenario]
	status := http.StatusOK
	if scenario == "error-details" {
		status = http.StatusBadRequest
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeRaw(w, status, body)
	}))
	defer srv.Close()

	ctx := context.Background()
	var err error
	switch scenario {
	case "data":
		c, _ := New(Config{BaseURL: srv.URL, Token: "t", TenantID: "acme"})
		_, err = c.Tags.Get(ctx, "t1")
		childReport(err, "nests deeper than", false)
	case "list":
		c, _ := New(Config{BaseURL: srv.URL, Token: "t", TenantID: "acme"})
		_, err = c.Tags.List(ctx, nil)
		childReport(err, "nests deeper than", false)
	case "error-details":
		// The envelope cannot be decoded, so the guard's refusal surfaces as the
		// envelope-less classification rather than as its own text.
		c, _ := New(Config{BaseURL: srv.URL, Token: "t", TenantID: "acme"})
		_, err = c.Tags.Get(ctx, "t1")
		if IsUnexpectedStatus(err) && !IsValidation(err) {
			fmt.Println(depthChildOK)
		} else {
			fmt.Printf("child: err = %.200v, want an envelope-less *APIError\n", err)
		}
	case "health":
		hc, _ := NewHealthClient(srv.URL)
		_, err = hc.Health.Live(ctx)
		childReport(err, "nests deeper than", false)
	default:
		fmt.Printf("child: unknown scenario %q\n", scenario)
	}
}

// --- the source guard ---------------------------------------------------------

// boundedDecoder is the one function allowed to call encoding/json's decoders.
const boundedDecoder = "decodeJSON"

// unboundedDecodes reports every reference to json.Unmarshal or json.NewDecoder
// outside decodeJSON in files, as "file:line: what".
//
// A REFERENCE, not only a call: `f := json.Unmarshal` followed by `f(...)` is the
// same bypass. And it fails closed on the two ways a reference could hide from a
// selector match -- a dot import, which makes Unmarshal a bare identifier, and
// an alias, which is resolved rather than assumed to be "json".
func unboundedDecodes(fset *token.FileSet, files map[string]*ast.File) []string {
	var found []string
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		file := files[name]
		local := ""
		for _, imp := range file.Imports {
			if path, _ := strconv.Unquote(imp.Path.Value); path != "encoding/json" {
				continue
			}
			local = "json"
			if imp.Name != nil {
				local = imp.Name.Name
			}
			if local == "." {
				found = append(found, fmt.Sprintf("%s: encoding/json is dot-imported, so its decoders cannot be told apart from local names", fset.Position(imp.Pos())))
			}
		}
		if local == "" || local == "." || local == "_" {
			continue
		}
		ast.Inspect(file, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if ok && fn.Name.Name == boundedDecoder && fn.Recv == nil {
				return false
			}
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); ok && x.Name == local && (sel.Sel.Name == "Unmarshal" || sel.Sel.Name == "NewDecoder") {
				found = append(found, fmt.Sprintf("%s: %s.%s outside %s", fset.Position(sel.Pos()), local, sel.Sel.Name, boundedDecoder))
			}
			return true
		})
	}
	return found
}

// Every decode of response bytes goes through decodeJSON, so the depth guard
// cannot be bypassed by a decoder written after it.
func TestEveryResponseDecodeIsDepthBounded(t *testing.T) {
	fset := token.NewFileSet()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]*ast.File{}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		files[path] = f
	}
	if len(files) == 0 {
		t.Fatal("no package source files found; the guard would pass vacuously")
	}
	if _, ok := files["jsondepth.go"]; !ok {
		t.Fatal("jsondepth.go not found; the guard is not looking at this package")
	}
	for _, v := range unboundedDecodes(fset, files) {
		t.Errorf("%s: decode response bytes with decodeJSON, which bounds their depth first", v)
	}
}

// On a correct tree the guard passes whether or not it works, so it is run
// against sources that break each rule.
func TestUnboundedDecodes_Fixtures(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want int
	}{
		{"clean", `package p
import "encoding/json"
func decodeJSON(b []byte, v interface{}) error { return json.Unmarshal(b, v) }
func use(b []byte, v interface{}) error { return decodeJSON(b, v) }`, 0},
		{"direct call", `package p
import "encoding/json"
func f(b []byte, v interface{}) error { return json.Unmarshal(b, v) }`, 1},
		{"a decoder", `package p
import ("bytes"; "encoding/json")
func f(b []byte, v interface{}) error { return json.NewDecoder(bytes.NewReader(b)).Decode(v) }`, 1},
		{"a func value", `package p
import "encoding/json"
var decode = json.Unmarshal`, 1},
		{"inside a method", `package p
import "encoding/json"
type T struct{}
func (T) UnmarshalJSON(b []byte) error { var m map[string]interface{}; return json.Unmarshal(b, &m) }`, 1},
		{"a method named decodeJSON is not the function", `package p
import "encoding/json"
type T struct{}
func (T) decodeJSON(b []byte, v interface{}) error { return json.Unmarshal(b, v) }`, 1},
		{"an aliased import", `package p
import enc "encoding/json"
func f(b []byte, v interface{}) error { return enc.Unmarshal(b, v) }`, 1},
		{"a dot import", `package p
import . "encoding/json"
func f(b []byte, v interface{}) error { return Unmarshal(b, v) }`, 1},
		{"a local named json is not the package", `package p
type codec struct{}
func (codec) Unmarshal([]byte, interface{}) error { return nil }
var json codec
func f(b []byte, v interface{}) error { return json.Unmarshal(b, v) }`, 0},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, "fixture.go", tt.src, 0)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			got := unboundedDecodes(fset, map[string]*ast.File{"fixture.go": f})
			if len(got) != tt.want {
				t.Errorf("found %d violations, want %d: %v", len(got), tt.want, got)
			}
		})
	}
}
