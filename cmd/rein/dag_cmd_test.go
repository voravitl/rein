package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const dagCmdValidSpec = `{"schema":"rein_dag_spec/v1","run":"r1","repo":"/abs/repo","base":"1a3d605635ba995c169e81a9f8079fcb34364474","goal":"one sentence","max_parallel":2,
 "nodes":[{"id":"api","kind":"worker","title":"api","write":["internal/api/**"],"depends_on":[],
   "prompt":{"task":"t","deliverable":"d","scope":"s","verify":"v","stop_when":"w"},"verify":[["go","test","./internal/api"]],"providers":["claude"],"inputs":[],"outputs":[]},
  {"id":"check","kind":"action","title":"check","write":[],"depends_on":["api"],
   "prompt":{"task":"t","deliverable":"d","scope":"s","verify":"v","stop_when":"w"},"verify":[["/bin/true"]],
   "action":{"argv":["go","test","./..."],"image":"sha256:632dff7351799a5a9954a48dc06670ade72ee44e8c351cdc0e5a57af2fea21af","timeout_ms":600000}}]}`

func writeDagSpec(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "spec.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

type dagLintJSON struct {
	Valid  bool `json:"valid"`
	Errors []struct {
		Code    string `json:"code"`
		Node    string `json:"node"`
		Message string `json:"message"`
	} `json:"errors"`
	Warnings []json.RawMessage `json:"warnings"`
}

func TestDagCmdLintExitCodes(t *testing.T) {
	good := writeDagSpec(t, dagCmdValidSpec)
	bad := writeDagSpec(t, strings.Replace(dagCmdValidSpec, `"max_parallel":2,`, `"max_parallel":2,"launches":[],`, 1))
	for _, c := range []struct {
		name string
		args []string
		want int
		out  string
	}{
		{"valid", []string{"lint", "--spec", good}, 0, "valid: 0 warning(s)"},
		{"refused", []string{"lint", "--spec", bad}, 1, "error unknown_field"},
		{"no-subcommand", nil, 2, ""},
		{"unknown-subcommand", []string{"frobnicate"}, 2, ""},
		{"missing-spec-flag", []string{"lint"}, 2, ""},
		{"extra-arg", []string{"lint", "--spec", good, "x"}, 2, ""},
		{"bad-flag", []string{"lint", "--nope"}, 2, ""},
		{"unreadable", []string{"lint", "--spec", filepath.Join(t.TempDir(), "absent.json")}, 2, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, code := capture(t, func() int { return cmdDag(c.args) })
			if code != c.want || !strings.Contains(out, c.out) {
				t.Fatalf("exit %d out %q; want %d containing %q", code, out, c.want, c.out)
			}
		})
	}
}

func TestDagCmdLintJSONShape(t *testing.T) {
	good := writeDagSpec(t, dagCmdValidSpec)
	out, code := capture(t, func() int { return cmdDag([]string{"lint", "--spec", good, "--json"}) })
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for k := range raw {
		keys = append(keys, k)
	}
	if len(keys) != 3 || raw["valid"] == nil || string(raw["errors"]) != "[]" || string(raw["warnings"]) != "[]" {
		t.Fatalf("shape: %s", out)
	}

	// Refused: several findings, sorted by code then node, stable across runs.
	bad := strings.Replace(dagCmdValidSpec, `"base":"1a3d605635ba995c169e81a9f8079fcb34364474"`, `"base":"HEAD"`, 1)
	bad = strings.Replace(bad, `"kind":"worker"`, `"kind":"agent","profile":{}`, 1)
	p := writeDagSpec(t, bad)
	out, code = capture(t, func() int { return cmdDag([]string{"lint", "--json", "--spec", p}) })
	if code != 1 {
		t.Fatalf("exit %d: %s", code, out)
	}
	var res dagLintJSON
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, e := range res.Errors {
		got = append(got, e.Code+"/"+e.Node)
	}
	want := []string{"invalid_base/", "invalid_kind/api", "unknown_field/api"}
	if res.Valid || !reflect.DeepEqual(got, want) || res.Warnings == nil {
		t.Fatalf("got %v valid=%v; want %v", got, res.Valid, want)
	}
	again, _ := capture(t, func() int { return cmdDag([]string{"lint", "--json", "--spec", p}) })
	if again != out {
		t.Fatalf("nondeterministic output:\n%s\n%s", out, again)
	}
}
