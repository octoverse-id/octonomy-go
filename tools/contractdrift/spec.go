package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// httpMethods is the set of path-item keys that are operations. A path item may
// also carry non-operation keys (`parameters`, `summary`, vendor extensions), so
// the loader selects rather than assumes: an unknown key must not be read as an
// operation with no responses and reported as a vanished endpoint.
var httpMethods = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true,
	"options": true, "head": true, "patch": true, "trace": true,
}

// Spec is the slice of an OpenAPI document this gate compares. It is deliberately
// NOT a full OpenAPI model: every comparison below runs over yaml.Node trees
// flattened to key/value lines, so a field this struct never names still takes
// part in the diff. Adding a typed field here would narrow what is compared, not
// widen it.
type Spec struct {
	// Path is where the document was read from, for report lines.
	Path string

	Version string

	// Operations is keyed by "METHOD path" -- e.g. "get /api/v2/tags".
	Operations map[string]*Operation

	// Schemas is components.schemas, one yaml.Node per schema name.
	Schemas map[string]*yaml.Node
}

// Operation is one path-item method.
type Operation struct {
	Method string
	Path   string

	// ID is operationId. Reported, never used as a comparison key: the server
	// generates it from the route name, so a rename would look like one endpoint
	// removed and another added. Path and method are the stable identity.
	ID string

	// Params maps a parameter name to its flattened definition.
	Params map[string]map[string]string

	// Responses maps a status code to its flattened definition.
	Responses map[string]map[string]string

	// RequestBody is the flattened requestBody node, nil when absent.
	RequestBody map[string]string

	// OKSchema describes what the spec documents as the 200 response body, in the
	// vocabulary docs/contract-coverage.yaml uses: "array", "ref:Tag", or "none".
	OKSchema string
}

// Key is the operation's identity in reports and in coverage lookups.
func (o *Operation) Key() string { return o.Method + " " + o.Path }

// document is the minimal typed shape needed to walk to the interesting nodes.
// Everything below those nodes stays a yaml.Node.
type document struct {
	Info struct {
		Version string `yaml:"version"`
	} `yaml:"info"`
	Paths      map[string]map[string]yaml.Node `yaml:"paths"`
	Components struct {
		Schemas map[string]yaml.Node `yaml:"schemas"`
	} `yaml:"components"`
}

// LoadSpec reads and indexes an OpenAPI document.
func LoadSpec(path string) (*Spec, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc document
	// KnownFields is deliberately off: the documents carry plenty this gate does
	// not model (servers, security, tags), and refusing to load them would turn
	// any server-side addition into a gate that cannot run rather than a gate
	// that reports.
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if doc.Info.Version == "" {
		return nil, fmt.Errorf("%s: info.version is empty -- this does not look like an Octonomy contract", path)
	}
	if len(doc.Paths) == 0 {
		return nil, fmt.Errorf("%s: no paths -- refusing to report an empty document as a contract", path)
	}

	spec := &Spec{
		Path:       path,
		Version:    doc.Info.Version,
		Operations: make(map[string]*Operation),
		Schemas:    make(map[string]*yaml.Node),
	}
	for p, item := range doc.Paths {
		for method, node := range item {
			if !httpMethods[strings.ToLower(method)] {
				continue
			}
			op, err := newOperation(strings.ToLower(method), p, node)
			if err != nil {
				return nil, fmt.Errorf("%s: %s %s: %w", path, method, p, err)
			}
			spec.Operations[op.Key()] = op
		}
	}
	for name, node := range doc.Components.Schemas {
		spec.Schemas[name] = &node
	}
	return spec, nil
}

func newOperation(method, path string, node yaml.Node) (*Operation, error) {
	op := &Operation{
		Method:    method,
		Path:      path,
		Params:    make(map[string]map[string]string),
		Responses: make(map[string]map[string]string),
		OKSchema:  "none",
	}

	var body struct {
		ID          string      `yaml:"operationId"`
		Parameters  []yaml.Node `yaml:"parameters"`
		Responses   yaml.Node   `yaml:"responses"`
		RequestBody yaml.Node   `yaml:"requestBody"`
	}
	// A path item that will not decode fails the run rather than yielding an
	// operation with no parameters and no responses. The first draft of this
	// swallowed the error and returned the empty operation, and every POST, PATCH,
	// and body-carrying DELETE in the contract silently lost its parameters and its
	// documented responses -- a comparison that reports nothing, which is the exact
	// failure this whole gate exists to prevent.
	if err := node.Decode(&body); err != nil {
		return nil, err
	}
	op.ID = body.ID

	for i := range body.Parameters {
		var p struct {
			Name string `yaml:"name"`
		}
		if err := body.Parameters[i].Decode(&p); err != nil || p.Name == "" {
			continue
		}
		flat := flatten(&body.Parameters[i])
		// `name` is the map key; leaving it in the value too would report every
		// rename twice.
		delete(flat, "name")
		op.Params[p.Name] = flat
	}

	if body.Responses.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(body.Responses.Content); i += 2 {
			status := body.Responses.Content[i].Value
			op.Responses[status] = flatten(body.Responses.Content[i+1])
		}
	}
	if body.RequestBody.Kind != 0 {
		op.RequestBody = flatten(&body.RequestBody)
	}
	op.OKSchema = okSchema(op.successResponse())
	return op, nil
}

// successResponse is the documented 2xx body, lowest status first. Creates
// document 201 (and /tag-assignments documents both 200 and 201), so keying the
// recorded response shape on 200 alone would read every create as undocumented.
func (o *Operation) successResponse() map[string]string {
	for _, status := range sortedKeys(o.Responses) {
		if len(status) == 3 && status[0] == '2' {
			return o.Responses[status]
		}
	}
	return nil
}

// okSchema names the shape the spec documents for a 200 body, in the vocabulary
// docs/contract-coverage.yaml records under `documented_response`.
//
// It reads the FLATTENED response, so it sees `content.application/json.schema.*`
// whatever media type wrapper the server generates around it.
func okSchema(resp map[string]string) string {
	if resp == nil {
		return "none"
	}
	// Sorted, so a body documented under two media types resolves the same way
	// on every run.
	keys := sortedKeys(resp)
	for _, k := range keys {
		if strings.HasSuffix(k, ".schema.$ref") {
			return "ref:" + strings.TrimPrefix(resp[k], "#/components/schemas/")
		}
	}
	for _, k := range keys {
		if strings.HasSuffix(k, ".schema.type") && resp[k] == "array" {
			return "array"
		}
	}
	for _, k := range keys {
		if strings.Contains(k, ".schema.") {
			return "other"
		}
	}
	return "none"
}

// flatten renders a yaml.Node as a sorted, comparable set of "path=value" pairs,
// so any change anywhere under it shows up as an added, removed, or changed line
// without this tool naming the field in advance. That is the point: a gate that
// compares a fixed list of fields reports only the drift its author predicted.
//
// Two deliberate normalizations:
//
//   - `description` is dropped at every level. Descriptions are prose, and the
//     server rewords them freely; reporting that as contract drift is the fastest
//     way to teach everyone to ignore this job.
//   - a sequence whose items are all scalars is compared as a SORTED set, because
//     the two places the specs use one -- `required` and `enum` -- are sets whose
//     generated order is incidental. Sequences of mappings keep their index.
func flatten(node *yaml.Node) map[string]string {
	out := make(map[string]string)
	flattenInto("", node, out)
	return out
}

func flattenInto(prefix string, node *yaml.Node, out map[string]string) {
	if node == nil {
		return
	}
	switch node.Kind {
	case yaml.DocumentNode:
		if len(node.Content) > 0 {
			flattenInto(prefix, node.Content[0], out)
		}
	case yaml.AliasNode:
		flattenInto(prefix, node.Alias, out)
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i].Value
			if key == "description" {
				continue
			}
			flattenInto(join(prefix, key), node.Content[i+1], out)
		}
	case yaml.SequenceNode:
		if scalars, ok := scalarSequence(node); ok {
			sort.Strings(scalars)
			out[prefix] = "[" + strings.Join(scalars, ", ") + "]"
			return
		}
		for i, child := range node.Content {
			flattenInto(fmt.Sprintf("%s[%d]", prefix, i), child, out)
		}
	default:
		out[prefix] = node.Value
	}
}

func scalarSequence(node *yaml.Node) ([]string, bool) {
	if len(node.Content) == 0 {
		return nil, true
	}
	values := make([]string, 0, len(node.Content))
	for _, child := range node.Content {
		if child.Kind != yaml.ScalarNode {
			return nil, false
		}
		values = append(values, child.Value)
	}
	return values, true
}

func join(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

// diffFlat compares two flattened nodes and returns one line per difference.
func diffFlat(was, now map[string]string) []string {
	var lines []string
	for _, key := range sortedKeys(now) {
		old, had := was[key]
		if !had {
			lines = append(lines, fmt.Sprintf("+ %s: %s", key, now[key]))
			continue
		}
		if old != now[key] {
			lines = append(lines, fmt.Sprintf("~ %s: %s -> %s", key, old, now[key]))
		}
	}
	for _, key := range sortedKeys(was) {
		if _, still := now[key]; !still {
			lines = append(lines, fmt.Sprintf("- %s: %s", key, was[key]))
		}
	}
	sort.Strings(lines)
	return lines
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
