package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// httpMethods is the set of path-item keys that are operations. A path item also
// carries non-operation keys -- `parameters`, `summary`, vendor extensions -- so
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

	// Params maps a parameter's identity to its flattened definition. OpenAPI
	// identifies a parameter by `in` AND `name` -- a header and a query parameter
	// may share a name -- so the key is "<in> <name>" and never the name alone.
	Params map[string]map[string]string

	// Responses maps a status code to its flattened definition.
	Responses map[string]map[string]string

	// RequestBody is the flattened requestBody node, nil when absent.
	RequestBody map[string]string

	// OKSchema describes what the spec documents as the success response body, in
	// the vocabulary docs/contract-coverage.yaml uses: "array", "ref:Tag",
	// "none", or "other".
	OKSchema string

	// OKModel is the component schema name behind the success body -- the `$ref`
	// itself, or an array's item `$ref`. Empty when the body is neither.
	OKModel string

	// RequestModel is the component schema name behind the request body. Empty on
	// an operation that documents none.
	RequestModel string
}

// ParamSchema returns the flattened `schema` of one documented parameter, with
// the `schema.` prefix stripped -- so `type`, `format` and `items.type` are read
// the same way wherever they come from.
func (o *Operation) ParamSchema(in, name string) map[string]string {
	flat, ok := o.Params[in+" "+name]
	if !ok {
		return nil
	}
	out := map[string]string{}
	for key, value := range flat {
		if rest, found := strings.CutPrefix(key, "schema."); found {
			out[rest] = value
		}
	}
	return out
}

// HeaderParams returns the operation's documented header parameter names.
func (o *Operation) HeaderParams() []string {
	var out []string
	for key := range o.Params {
		if in, name, ok := strings.Cut(key, " "); ok && in == "header" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// Key is the operation's identity in reports and in coverage lookups.
func (o *Operation) Key() string { return o.Method + " " + o.Path }

// QueryParams returns the operation's query parameter names.
func (o *Operation) QueryParams() []string {
	var out []string
	for key := range o.Params {
		if in, name, ok := strings.Cut(key, " "); ok && in == "query" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// document is the minimal typed shape needed to walk to the interesting nodes.
// Everything below those nodes stays a yaml.Node.
type document struct {
	Info struct {
		Version string `yaml:"version"`
	} `yaml:"info"`
	Paths      map[string]map[string]yaml.Node `yaml:"paths"`
	Components struct {
		Schemas    map[string]yaml.Node `yaml:"schemas"`
		Parameters map[string]yaml.Node `yaml:"parameters"`
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
		// Parameters declared on the PATH ITEM apply to every operation under it.
		// OpenAPI allows this and drf-spectacular does not currently emit it --
		// which is exactly why it is handled here rather than when it first
		// appears: a parameter this loader cannot see is a parameter the gate
		// reports as absent from a contract that documents it.
		shared, err := collectParams(&doc, item["parameters"])
		if err != nil {
			return nil, fmt.Errorf("%s: %s: path-item parameters: %w", path, p, err)
		}
		for method, node := range item {
			if !httpMethods[strings.ToLower(method)] {
				continue
			}
			op, err := newOperation(&doc, strings.ToLower(method), p, node, shared)
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

func newOperation(doc *document, method, path string, node yaml.Node, shared map[string]map[string]string) (*Operation, error) {
	op := &Operation{
		Method:    method,
		Path:      path,
		Params:    make(map[string]map[string]string),
		Responses: make(map[string]map[string]string),
		OKSchema:  "none",
	}
	for key, value := range shared {
		op.Params[key] = value
	}

	var body struct {
		ID          string    `yaml:"operationId"`
		Parameters  yaml.Node `yaml:"parameters"`
		Responses   yaml.Node `yaml:"responses"`
		RequestBody yaml.Node `yaml:"requestBody"`
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

	own, err := collectParams(doc, body.Parameters)
	if err != nil {
		return nil, err
	}
	for key, value := range own {
		// An operation's own parameter overrides the path item's, per OpenAPI.
		op.Params[key] = value
	}

	if body.Responses.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(body.Responses.Content); i += 2 {
			status := body.Responses.Content[i].Value
			op.Responses[status] = flatten(body.Responses.Content[i+1])
		}
	}
	if body.RequestBody.Kind != 0 {
		op.RequestBody = flatten(&body.RequestBody)
		op.RequestModel = refIn(op.RequestBody)
	}
	op.OKSchema, op.OKModel = okSchema(op.successResponse())
	return op, nil
}

// collectParams flattens a `parameters` list, resolving component references and
// keying each entry by "<in> <name>".
//
// It fails on a parameter it cannot identify. A `$ref` that does not resolve, or
// an entry with no `name`, used to be skipped silently -- and a skipped parameter
// is one the gate reports as neither added, removed, nor unimplemented.
func collectParams(doc *document, node yaml.Node) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	if node.Kind == 0 {
		return out, nil // absent, which is ordinary
	}
	if node.Kind != yaml.SequenceNode {
		// Present and not a list. Returning an empty set here would report every
		// parameter on the operation as removed, or as never implemented, from a
		// document the gate simply failed to read.
		return nil, fmt.Errorf("`parameters` is not a list")
	}
	for i, entry := range node.Content {
		resolved, err := resolveParam(doc, entry)
		if err != nil {
			return nil, fmt.Errorf("parameter %d: %w", i, err)
		}
		var head struct {
			Name string `yaml:"name"`
			In   string `yaml:"in"`
		}
		if err := resolved.Decode(&head); err != nil {
			return nil, fmt.Errorf("parameter %d: %w", i, err)
		}
		if head.Name == "" || head.In == "" {
			return nil, fmt.Errorf("parameter %d has no name or no `in` -- the gate cannot identify it", i)
		}
		flat := flatten(resolved)
		// `name` and `in` are the map key; leaving them in the value too would
		// report every rename twice.
		delete(flat, "name")
		delete(flat, "in")
		out[head.In+" "+head.Name] = flat
	}
	return out, nil
}

// resolveParam follows a local `#/components/parameters/...` reference. A
// reference anywhere else is an error rather than a shrug: this gate compares two
// documents it can read in full, or it says it could not.
func resolveParam(doc *document, entry *yaml.Node) (*yaml.Node, error) {
	var head struct {
		Ref string `yaml:"$ref"`
	}
	if err := entry.Decode(&head); err != nil || head.Ref == "" {
		return entry, nil
	}
	const prefix = "#/components/parameters/"
	if !strings.HasPrefix(head.Ref, prefix) {
		return nil, fmt.Errorf("cannot resolve %q -- only local component parameters are supported", head.Ref)
	}
	name := strings.TrimPrefix(head.Ref, prefix)
	target, ok := doc.Components.Parameters[name]
	if !ok {
		return nil, fmt.Errorf("%q does not resolve: components.parameters has no %q", head.Ref, name)
	}
	return &target, nil
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

// okSchema names the shape the spec documents for a success body, in the
// vocabulary docs/contract-coverage.yaml records under `documented_response`, and
// the component schema behind it.
//
// It reads the FLATTENED response, so it sees `content.application/json.schema.*`
// whatever media type wrapper the server generates around it.
func okSchema(resp map[string]string) (shape, model string) {
	if resp == nil {
		return "none", ""
	}
	// Sorted, so a body documented under two media types resolves the same way
	// on every run.
	keys := sortedKeys(resp)
	for _, k := range keys {
		if strings.HasSuffix(k, ".schema.$ref") {
			return "ref:" + schemaName(resp[k]), schemaName(resp[k])
		}
	}
	for _, k := range keys {
		if strings.HasSuffix(k, ".schema.type") && resp[k] == "array" {
			// The item reference, when there is one: a list of Tag decodes into
			// the same model a single Tag does.
			for _, item := range keys {
				if strings.HasSuffix(item, ".schema.items.$ref") {
					return "array", schemaName(resp[item])
				}
			}
			return "array", ""
		}
	}
	for _, k := range keys {
		if strings.Contains(k, ".schema.") {
			return "other", ""
		}
	}
	return "none", ""
}

// refIn finds the component schema a flattened node references. Sorted, so a body
// documented under two media types resolves the same way on every run.
func refIn(flat map[string]string) string {
	for _, k := range sortedKeys(flat) {
		if strings.HasSuffix(k, ".schema.$ref") {
			return schemaName(flat[k])
		}
	}
	return ""
}

// PropertySchema returns the flattened schema of one property of a component
// schema, in the same shape ParamSchema returns.
func (s *Spec) PropertySchema(schema, property string) map[string]string {
	node, ok := s.Schemas[schema]
	if !ok {
		return nil
	}
	root := node
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		root = root.Content[0]
	}
	props := mappingValue(root, "properties")
	if props == nil {
		return nil
	}
	for i := 0; i+1 < len(props.Content); i += 2 {
		if props.Content[i].Value == property {
			return flatten(props.Content[i+1])
		}
	}
	return nil
}

func schemaName(ref string) string {
	return strings.TrimPrefix(ref, "#/components/schemas/")
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
//   - SEQUENCES ARE COMPARED AS SETS. A scalar sequence is sorted -- `required`
//     and `enum` are sets whose generated order is incidental -- and a sequence of
//     mappings is sorted by its own rendered content before being indexed, so a
//     generator that reorders `oneOf` or `allOf` members reports nothing while a
//     genuine change to one of them still reports.
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
		for i, child := range sortedSequence(node) {
			flattenInto(fmt.Sprintf("%s[%d]", prefix, i), child, out)
		}
	default:
		out[prefix] = node.Value
	}
}

// sortedSequence orders a sequence of non-scalars by its own rendered content, so
// position in the list stops being part of the comparison.
func sortedSequence(node *yaml.Node) []*yaml.Node {
	items := make([]*yaml.Node, len(node.Content))
	copy(items, node.Content)
	rendered := make(map[*yaml.Node]string, len(items))
	for _, item := range items {
		flat := flatten(item)
		parts := make([]string, 0, len(flat))
		for _, k := range sortedKeys(flat) {
			parts = append(parts, k+"="+flat[k])
		}
		rendered[item] = strings.Join(parts, "\x00")
	}
	sort.SliceStable(items, func(i, j int) bool { return rendered[items[i]] < rendered[items[j]] })
	return items
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
