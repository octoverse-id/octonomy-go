package main

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Coverage is docs/contract-coverage.yaml: the SDK's own statement of what it
// implements, what it deliberately does not, and which recorded divergences from
// the generated spec are still expected to be true.
//
// It exists because "not implemented" has to be a decision someone wrote down.
// An endpoint the SDK simply never noticed and an endpoint the SDK decided to
// skip look identical from the outside, and that is precisely how this SDK sat
// on a server 1.0.0 contract while the server shipped 3.1.0.
//
// Every row is asserted against the contracts AND against the Go sources, so a
// row cannot drift into fiction: the gate derives each method's route, transport
// helper, query parameters, and decoded model from the code itself and compares
// them with what the row claims.
type Coverage struct {
	Path string `yaml:"-"`

	// Operations is every operation the vendored contracts publish, each either
	// implemented (naming the Go method) or unimplemented (naming a reason).
	Operations []CoverageOperation `yaml:"operations"`

	// UnsentQueryParameters lists query parameters the contracts document that the
	// SDK deliberately never sends, PER OPERATION. Scoped that way because a name
	// is not a decision: `limit` would be legitimately unsent on /tag-resolution
	// and is legitimately sent on every list route, and a name-keyed allowlist
	// would silence both.
	UnsentQueryParameters []UnsentParameter `yaml:"unsent_query_parameters"`

	// UndocumentedModelFields lists JSON fields an SDK response model decodes that
	// the contract's schema does not document, each with the reason it is there.
	UndocumentedModelFields []UndocumentedField `yaml:"undocumented_model_fields"`

	// ServerErrorCodes is the server's error registry, vendored the way the two
	// contracts are -- and for the same reason. The registry is not in the OpenAPI
	// documents (ErrorResponse types `code` as a bare string), so without a copy
	// here the only comparison possible would be SDK-against-upstream, which runs
	// weekly and needs the network. Vendoring it turns one unverifiable hop into
	// two checked ones: this list against errors.go offline, and this list against
	// the real registry on the schedule.
	ServerErrorCodes []string `yaml:"server_error_codes"`

	// SDKOnlyErrorCodes lists Code* constants that exist in errors.go with no
	// counterpart in the server's error registry, each with the reason it is
	// legitimate.
	SDKOnlyErrorCodes []SDKOnlyCode `yaml:"sdk_only_error_codes"`
}

// CoverageOperation is one row of the inventory.
type CoverageOperation struct {
	// Path is the version-independent suffix -- "/tags", not "/api/v2/tags". The
	// SDK's resource files build exactly that suffix and the transport prepends
	// /api/<version>, so one row covers the operation on both surfaces. A surface
	// that publishes it while the other does not is reported separately, by
	// checkSurfaceParity -- one row cannot be true of both surfaces if only one of
	// them has the operation.
	Path   string `yaml:"path"`
	Method string `yaml:"method"`

	// SDK names the Go method as Receiver.Method, and File the file declaring it.
	// Both empty when Unimplemented is set. The gate derives that method's real
	// route and compares it with Path and Method above, so naming the wrong method
	// is a finding rather than a pass.
	SDK  string `yaml:"sdk"`
	File string `yaml:"file"`

	// Unimplemented is the recorded reason this operation has no SDK method. Its
	// presence is what makes the gap a decision instead of an oversight.
	Unimplemented string `yaml:"unimplemented"`

	// DocumentedResponse is the success-body shape the VENDORED spec documents --
	// its lowest 2xx, since creates document 201 and /tag-assignments documents
	// both: "array", "ref:<Schema>", "none", or "other". The gate asserts the spec
	// still says this, which is what keeps the divergence below a tracked fact
	// rather than a silent suppression.
	DocumentedResponse string `yaml:"documented_response"`

	// ActualResponse is what the running server really returns, in the SDK's own
	// vocabulary -- the closed set in actualResponses below. It is checked against
	// the transport helper the method calls (doList yields a list envelope, doData
	// a data envelope, and so on), so it states what the code does rather than what
	// someone remembered. Where it differs from DocumentedResponse the server
	// wins: every one of these was verified against a booted server, and the
	// spec's generator cannot see the envelope because a renderer adds it below
	// the serializers.
	ActualResponse string `yaml:"actual_response"`
}

// Key matches Operation.Key once the surface prefix is stripped.
func (c CoverageOperation) Key() string { return c.Method + " " + c.Path }

// UnsentParameter records a documented query parameter one operation does not
// send.
type UnsentParameter struct {
	Path   string `yaml:"path"`
	Method string `yaml:"method"`
	Name   string `yaml:"name"`
	Reason string `yaml:"reason"`
}

// Key is the operation key plus the parameter name.
func (u UnsentParameter) Key() string { return u.Method + " " + u.Path + " " + u.Name }

// UndocumentedField records a JSON field an SDK model decodes that the contract
// does not document.
type UndocumentedField struct {
	Model  string `yaml:"model"`
	Field  string `yaml:"field"`
	Reason string `yaml:"reason"`
}

// Key is "Model.field".
func (u UndocumentedField) Key() string { return u.Model + "." + u.Field }

// SDKOnlyCode records an errors.go constant with no server counterpart.
type SDKOnlyCode struct {
	Code   string `yaml:"code"`
	Reason string `yaml:"reason"`
}

// The two response vocabularies are closed sets, validated on load. A typo in
// either field would otherwise read as a shape nothing in the spec can ever
// match, and the row would report drift on every run until someone re-read it.
var (
	documentedResponseRE = regexp.MustCompile(`^(array|none|other|ref:[A-Za-z0-9_]+)$`)

	actualResponses = map[string]bool{
		"list-envelope":      true, // {"data": [...], "pagination": {...}}
		"data-envelope":      true, // {"data": {...}}
		"composite-envelope": true, // {"data": {...counts, items}} -- bulk and replace
		"bare":               true, // no envelope at all -- the health probes
		"none":               true, // no body -- 204
	}
	actualResponseList = "list-envelope, data-envelope, composite-envelope, bare, none"

	httpMethodNames = map[string]bool{
		"get": true, "put": true, "post": true, "delete": true,
		"options": true, "head": true, "patch": true, "trace": true,
	}
)

// LoadCoverage reads and validates the inventory.
func LoadCoverage(path string) (*Coverage, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cov Coverage
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	// KnownFields ON, unlike the specs: this file is ours, a typo'd key here is a
	// row that silently does nothing, and a coverage row that does nothing is a
	// gate that reports green over an unreviewed endpoint.
	dec.KnownFields(true)
	if err := dec.Decode(&cov); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	cov.Path = path
	if len(cov.Operations) == 0 {
		return nil, fmt.Errorf("%s: no operations -- an empty inventory would make every endpoint look declared", path)
	}

	seen := make(map[string]bool, len(cov.Operations))
	for _, op := range cov.Operations {
		switch {
		case op.Path == "" || op.Method == "":
			return nil, fmt.Errorf("%s: an operation row is missing path or method", path)
		case !httpMethodNames[op.Method]:
			return nil, fmt.Errorf("%s: %s: %q is not a lowercase HTTP method", path, op.Key(), op.Method)
		case !strings.HasPrefix(op.Path, "/"):
			return nil, fmt.Errorf("%s: %s: path must be the version-independent suffix, starting with /", path, op.Key())
		case strings.HasPrefix(op.Path, "/api/"):
			return nil, fmt.Errorf("%s: %s: path must not carry the /api/<version> prefix", path, op.Key())
		case seen[op.Key()]:
			return nil, fmt.Errorf("%s: %s is listed twice", path, op.Key())
		case op.Unimplemented == "" && (op.SDK == "" || op.File == ""):
			return nil, fmt.Errorf("%s: %s: needs either sdk+file or an unimplemented reason", path, op.Key())
		case op.Unimplemented != "" && op.SDK != "":
			return nil, fmt.Errorf("%s: %s: cannot be both implemented and unimplemented", path, op.Key())
		case op.SDK != "" && !strings.Contains(op.SDK, "."):
			return nil, fmt.Errorf("%s: %s: sdk must name the method as Receiver.Method", path, op.Key())
		case op.DocumentedResponse == "" || op.ActualResponse == "":
			return nil, fmt.Errorf("%s: %s: needs documented_response and actual_response", path, op.Key())
		case !documentedResponseRE.MatchString(op.DocumentedResponse):
			return nil, fmt.Errorf("%s: %s: documented_response %q is not `array`, `none`, `other`, or `ref:<Schema>`", path, op.Key(), op.DocumentedResponse)
		case !actualResponses[op.ActualResponse]:
			return nil, fmt.Errorf("%s: %s: actual_response %q is not one of %s", path, op.Key(), op.ActualResponse, actualResponseList)
		}
		seen[op.Key()] = true
	}

	for _, p := range cov.UnsentQueryParameters {
		if p.Path == "" || p.Method == "" || p.Name == "" || p.Reason == "" {
			return nil, fmt.Errorf("%s: unsent_query_parameters needs path, method, name, and reason on every row", path)
		}
		if !seen[p.Method+" "+p.Path] {
			return nil, fmt.Errorf("%s: unsent_query_parameters names %q, which is not an operation in this file", path, p.Method+" "+p.Path)
		}
	}
	for _, f := range cov.UndocumentedModelFields {
		if f.Model == "" || f.Field == "" || f.Reason == "" {
			return nil, fmt.Errorf("%s: undocumented_model_fields needs model, field, and reason on every row", path)
		}
	}
	if len(cov.ServerErrorCodes) < minServerErrorCodes {
		return nil, fmt.Errorf("%s: server_error_codes lists only %d codes (expected at least %d) -- the vendored registry is the offline half of the error-code check and an empty one checks nothing",
			path, len(cov.ServerErrorCodes), minServerErrorCodes)
	}
	vendored := make(map[string]bool, len(cov.ServerErrorCodes))
	for _, code := range cov.ServerErrorCodes {
		if vendored[code] {
			return nil, fmt.Errorf("%s: server_error_codes lists %q twice", path, code)
		}
		vendored[code] = true
	}
	for _, c := range cov.SDKOnlyErrorCodes {
		if c.Code == "" || c.Reason == "" {
			return nil, fmt.Errorf("%s: sdk_only_error_codes needs a code and a reason on every row", path)
		}
		if vendored[c.Code] {
			return nil, fmt.Errorf("%s: %q is listed under both server_error_codes and sdk_only_error_codes -- it cannot be both the server's and the SDK's alone", path, c.Code)
		}
	}
	return &cov, nil
}

// ByKey indexes the inventory for lookup against a spec operation.
func (c *Coverage) ByKey() map[string]CoverageOperation {
	out := make(map[string]CoverageOperation, len(c.Operations))
	for _, op := range c.Operations {
		out[op.Key()] = op
	}
	return out
}

// UnsentIndex returns the allowlisted operation+parameter pairs and their reasons.
func (c *Coverage) UnsentIndex() map[string]string {
	out := make(map[string]string, len(c.UnsentQueryParameters))
	for _, p := range c.UnsentQueryParameters {
		out[p.Key()] = p.Reason
	}
	return out
}

// UndocumentedFieldIndex returns the allowlisted model fields and their reasons.
func (c *Coverage) UndocumentedFieldIndex() map[string]string {
	out := make(map[string]string, len(c.UndocumentedModelFields))
	for _, f := range c.UndocumentedModelFields {
		out[f.Key()] = f.Reason
	}
	return out
}

// ServerErrorCodeSet returns the vendored registry as a set.
func (c *Coverage) ServerErrorCodeSet() map[string]bool {
	out := make(map[string]bool, len(c.ServerErrorCodes))
	for _, code := range c.ServerErrorCodes {
		out[code] = true
	}
	return out
}

// SDKOnlyCodeSet returns the allowlisted SDK-only codes as a set.
func (c *Coverage) SDKOnlyCodeSet() map[string]string {
	out := make(map[string]string, len(c.SDKOnlyErrorCodes))
	for _, code := range c.SDKOnlyErrorCodes {
		out[code.Code] = code.Reason
	}
	return out
}
