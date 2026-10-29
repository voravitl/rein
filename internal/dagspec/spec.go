// Package dagspec parses and lints rein_dag_spec/v1: the agent-written DAG
// description that `rein dag compile` turns into an owner-approved schema-3
// supervisor plan. The spec carries no launch argv, profile, config or hash
// binding; those come from owner config at compile time, so every unknown or
// forbidden field is refused at every nesting level.
package dagspec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
)

// SchemaV1 is the only accepted spec schema.
const SchemaV1 = "rein_dag_spec/v1"

// MaxSpecBytes bounds how much of a spec file is read.
const MaxSpecBytes = 8 << 20

// Spec is a decoded rein_dag_spec/v1 document.
type Spec struct {
	Schema      string `json:"schema"`
	Run         string `json:"run"`
	Repo        string `json:"repo"`
	Base        string `json:"base"`
	Goal        string `json:"goal"`
	MaxParallel int    `json:"max_parallel"`
	Nodes       []Node `json:"nodes"`
}

// Node is one worker or action in the graph.
type Node struct {
	ID        string     `json:"id"`
	Kind      string     `json:"kind"`
	Title     string     `json:"title"`
	Write     []string   `json:"write"`
	DependsOn []string   `json:"depends_on"`
	Prompt    *Prompt    `json:"prompt"`
	Verify    [][]string `json:"verify"`
	Providers []string   `json:"providers"`
	Inputs    []Input    `json:"inputs"`
	Outputs   []Output   `json:"outputs"`
	Action    *Action    `json:"action"`
}

// Prompt holds the five mandatory prompt sections.
type Prompt struct {
	Task        string `json:"task"`
	Deliverable string `json:"deliverable"`
	Scope       string `json:"scope"`
	Verify      string `json:"verify"`
	StopWhen    string `json:"stop_when"`
}

// Action is the sandboxed command of an action node.
type Action struct {
	Argv      []string `json:"argv"`
	Image     string   `json:"image"`
	TimeoutMS int      `json:"timeout_ms"`
}

// Input names an artifact produced by a direct dependency.
type Input struct {
	Producer string `json:"producer"`
	Name     string `json:"name"`
	As       string `json:"as"`
}

// Output declares an artifact a node produces.
type Output struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	MaxBytes int64  `json:"max_bytes"`
}

// forbidden are owner-only fields; the spec may never carry them.
var forbidden = map[string]bool{
	"launches": true, "profile": true, "config_path": true, "config_digest": true,
	"process_policy": true, "contract_hash": true,
}

// shape describes the accepted keys of one JSON level. A nil shape is a leaf:
// any value is accepted there and the typed decode checks its type.
type shape struct {
	fields map[string]*shape
	elem   *shape
}

func obj(f map[string]*shape) *shape { return &shape{fields: f} }
func arr(e *shape) *shape            { return &shape{elem: e} }

var specShape = func() *shape {
	prompt := obj(map[string]*shape{"task": nil, "deliverable": nil, "scope": nil, "verify": nil, "stop_when": nil})
	action := obj(map[string]*shape{"argv": nil, "image": nil, "timeout_ms": nil})
	input := obj(map[string]*shape{"producer": nil, "name": nil, "as": nil})
	output := obj(map[string]*shape{"name": nil, "path": nil, "max_bytes": nil})
	node := obj(map[string]*shape{
		"id": nil, "kind": nil, "title": nil, "write": nil, "depends_on": nil, "prompt": prompt,
		"verify": nil, "providers": nil, "inputs": arr(input), "outputs": arr(output), "action": action,
	})
	return obj(map[string]*shape{
		"schema": nil, "run": nil, "repo": nil, "base": nil, "goal": nil, "max_parallel": nil, "nodes": arr(node),
	})
}()

type unknownKey struct {
	node int // index into nodes, -1 at spec level
	path string
	key  string
}

type walker struct {
	dec     *json.Decoder
	unknown []unknownKey
}

// Parse strictly decodes data. Structural problems (bad JSON, wrong types,
// duplicate keys, trailing data) return a nil spec and one invalid_spec
// finding. Unknown or forbidden keys are reported as unknown_field findings
// alongside the decoded spec so the remaining rules can still run.
func Parse(data []byte) (*Spec, []Finding) {
	if len(data) > MaxSpecBytes {
		return nil, []Finding{{Code: CodeInvalidSpec, Message: fmt.Sprintf("spec exceeds %d bytes", MaxSpecBytes)}}
	}
	w := &walker{dec: json.NewDecoder(bytes.NewReader(data))}
	w.dec.UseNumber()
	if err := w.walkTop(); err != nil {
		return nil, []Finding{{Code: CodeInvalidSpec, Message: err.Error()}}
	}
	var s Spec
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, []Finding{{Code: CodeInvalidSpec, Message: "decode: " + err.Error()}}
	}
	var out []Finding
	for _, u := range w.unknown {
		f := Finding{Code: CodeUnknownField, Message: fmt.Sprintf("unknown field %q at %s", u.key, u.path)}
		if forbidden[u.key] {
			f.Message = fmt.Sprintf("forbidden field %q at %s (owner config only)", u.key, u.path)
		}
		if u.node >= 0 && u.node < len(s.Nodes) {
			f.Node = s.Nodes[u.node].ID
		}
		out = append(out, f)
	}
	return &s, out
}

func (w *walker) walkTop() error {
	tok, err := w.dec.Token()
	if err != nil {
		return fmt.Errorf("decode: %v", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return errors.New("spec must be a JSON object")
	}
	if err := w.walkObject(specShape, "$", -1); err != nil {
		return err
	}
	if _, err := w.dec.Token(); err != io.EOF {
		return errors.New("trailing data after spec object")
	}
	return nil
}

// walkValue consumes one value. sh is nil for leaves.
func (w *walker) walkValue(sh *shape, path string, node int) error {
	tok, err := w.dec.Token()
	if err != nil {
		return fmt.Errorf("decode %s: %v", path, err)
	}
	d, isDelim := tok.(json.Delim)
	switch {
	case sh != nil && sh.fields != nil && isDelim && d == '{':
		return w.walkObject(sh, path, node)
	case sh != nil && sh.elem != nil && isDelim && d == '[':
		for i := 0; w.dec.More(); i++ {
			n := node
			if path == "$.nodes" {
				n = i
			}
			if err := w.walkValue(sh.elem, path+"["+strconv.Itoa(i)+"]", n); err != nil {
				return err
			}
		}
		_, err := w.dec.Token()
		return err
	case isDelim:
		// A leaf holding a container, or a container of the wrong kind: skip it and
		// let the typed decode report any type mismatch.
		return w.skipContainer(path)
	}
	return nil
}

func (w *walker) walkObject(sh *shape, path string, node int) error {
	seen := map[string]bool{}
	for w.dec.More() {
		tok, err := w.dec.Token()
		if err != nil {
			return fmt.Errorf("decode %s: %v", path, err)
		}
		key, _ := tok.(string)
		if seen[key] {
			return fmt.Errorf("duplicate key %q at %s", key, path)
		}
		seen[key] = true
		sub, ok := sh.fields[key]
		if !ok {
			w.unknown = append(w.unknown, unknownKey{node: node, path: path, key: key})
		}
		if err := w.walkValue(sub, path+"."+key, node); err != nil {
			return err
		}
	}
	_, err := w.dec.Token()
	return err
}

func (w *walker) skipContainer(path string) error {
	for depth := 1; depth > 0; {
		tok, err := w.dec.Token()
		if err != nil {
			return fmt.Errorf("decode %s: %v", path, err)
		}
		if d, ok := tok.(json.Delim); ok {
			if d == '{' || d == '[' {
				depth++
			} else {
				depth--
			}
		}
	}
	return nil
}
