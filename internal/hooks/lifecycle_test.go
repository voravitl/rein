package hooks

// Lifecycle plan: prove ownership before edits; merge shared hook documents,
// reject foreign standalone files, and remove only this task's generated hooks.
import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallPreservesForeignConflicts(t *testing.T) {
	root, wt, c := setup(t)
	cases := map[string]string{"agy": ".agents/hooks.json", "kiro": ".kiro/agents/rein.json", "opencode": ".opencode/plugins/rein/server.js"}
	for vendor, rel := range cases {
		path := filepath.Join(wt, rel)
		os.MkdirAll(filepath.Dir(path), 0755)
		body := `{"rein":{"foreign":true},"name":"mine"}`
		os.WriteFile(path, []byte(body), 0644)
		res, _, err := Install(c, filepath.Join(root, "rein"), []string{vendor})
		if err != nil || res[0].Err == nil {
			t.Errorf("%s should reject foreign destination: %v %+v", vendor, err, res)
		}
		got, _ := os.ReadFile(path)
		if string(got) != body {
			t.Errorf("%s foreign content modified", vendor)
		}
	}
}

func TestInstallPreservesForeignMention(t *testing.T) {
	root, wt, c := setup(t)
	path := filepath.Join(wt, ".codex/hooks.json")
	os.MkdirAll(filepath.Dir(path), 0755)
	os.WriteFile(path, []byte(`{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"echo 'hook --vendor codex'"}]}]}}`), 0644)
	Install(c, filepath.Join(root, "rein"), []string{"codex"})
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "echo 'hook --vendor codex'") {
		t.Fatal("foreign mention removed")
	}
}

func TestUninstallRoundTripPreservesOtherOwners(t *testing.T) {
	root, wt, c := setup(t)
	path := filepath.Join(wt, ".codex/hooks.json")
	os.MkdirAll(filepath.Dir(path), 0755)
	os.WriteFile(path, []byte(`{"other":1,"hooks":{"PreToolUse":[{"note":"foreign","hooks":[{"command":"echo 'hook --vendor codex'"}]}]}}`), 0644)
	for cycle := 0; cycle < 2; cycle++ {
		results, _, err := Install(c, filepath.Join(root, "rein"), All)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range results {
			if r.Err != nil {
				t.Fatal(r.Err)
			}
		}
		if c.HooksGeneration == "" {
			t.Fatal("missing generation")
		}
		// A mixed group must retain metadata and its foreign sibling after removal.
		doc := mustJSON(t, path)
		groups := doc["hooks"].(map[string]any)["PreToolUse"].([]any)
		own := groups[1].(map[string]any)["hooks"].([]any)[0]
		groups[0].(map[string]any)["hooks"] = append(groups[0].(map[string]any)["hooks"].([]any), own)
		writeJSON(path, doc)
		results, _, err = Uninstall(c, All)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range results {
			if r.Err != nil {
				t.Fatal(r.Err)
			}
		}
		if len(c.HooksInstalled) != 0 || c.HooksGeneration != "" || c.HooksInstalledAt != "" {
			t.Fatalf("stale metadata: %+v", c)
		}
		doc = mustJSON(t, path)
		b, _ := os.ReadFile(path)
		if doc["other"] == nil || !strings.Contains(string(b), "foreign") || strings.Contains(string(b), "--task w") {
			t.Fatalf("foreign data or residual hook: %s", b)
		}
		for _, rel := range []string{".kiro/agents/rein.json", ".opencode/plugins/rein/server.js", ".opencode/plugins/rein/package.json"} {
			if _, err := os.Lstat(filepath.Join(wt, rel)); !os.IsNotExist(err) {
				t.Fatalf("residual %s: %v", rel, err)
			}
		}
		if _, _, err := Uninstall(c, All); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUninstallRefusesTamperSymlinkAndTracked(t *testing.T) {
	for _, kind := range []string{"tamper", "symlink", "tracked"} {
		t.Run(kind, func(t *testing.T) {
			root, wt, c := setup(t)
			Install(c, filepath.Join(root, "rein"), []string{"opencode"})
			path := filepath.Join(wt, ".opencode/plugins/rein/server.js")
			switch kind {
			case "tamper":
				f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
				f.WriteString("// foreign customization\n")
				f.Close()
			case "symlink":
				outside := filepath.Join(root, "outside.js")
				os.WriteFile(outside, []byte("foreign"), 0644)
				os.Remove(path)
				if err := os.Symlink(outside, path); err != nil {
					t.Skip(err)
				}
			case "tracked":
				git(t, "-C", wt, "add", "-f", ".opencode/plugins/rein/server.js")
			}
			before, _ := os.ReadFile(path)
			res, _, err := Uninstall(c, []string{"opencode"})
			if err != nil || res[0].Err == nil {
				t.Fatalf("expected fail closed: %+v %v", res, err)
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) || len(c.HooksInstalled) != 1 {
				t.Fatal("refused uninstall changed state")
			}
		})
	}
}

func TestSharedDocumentMalformedAndNoopPreserveBytes(t *testing.T) {
	for _, body := range []string{`{"other": 1}`, `{"hooks":null}`, `{"hooks":{"PreToolUse":{}}}`, `{"hooks":{"PreToolUse":[{"hooks":false}]}}`, `{"hooks":{"PreToolUse":[false]}}`, `{"hooks":{"PreToolUse":[{"hooks":[false]}]}}`} {
		t.Run(body, func(t *testing.T) {
			root, wt, c := setup(t)
			path := filepath.Join(wt, ".codex/hooks.json")
			os.MkdirAll(filepath.Dir(path), 0755)
			os.WriteFile(path, []byte(body), 0644)
			results, _, err := Uninstall(c, []string{"codex"})
			if err != nil {
				t.Fatal(err)
			}
			if body != `{"other": 1}` && results[0].Err == nil {
				t.Fatal("malformed uninstall accepted")
			}
			got, _ := os.ReadFile(path)
			if string(got) != body {
				t.Fatalf("uninstall changed foreign bytes: %s", got)
			}
			if body != `{"other": 1}` {
				results, _, err = Install(c, filepath.Join(root, "rein"), []string{"codex"})
				if err != nil || results[0].Err == nil {
					t.Fatal("malformed install accepted")
				}
				got, _ = os.ReadFile(path)
				if string(got) != body {
					t.Fatal("install changed foreign bytes")
				}
			}
		})
	}
}

func TestInstallRefusesEmptyForeignAgent(t *testing.T) {
	root, wt, c := setup(t)
	path := filepath.Join(wt, ".kiro/agents/rein.json")
	os.MkdirAll(filepath.Dir(path), 0755)
	os.WriteFile(path, []byte(`{}`), 0644)
	results, _, err := Install(c, filepath.Join(root, "rein"), []string{"kiro"})
	if err != nil || results[0].Err == nil {
		t.Fatalf("empty foreign agent overwritten: %+v %v", results, err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "{}" {
		t.Fatal("foreign agent changed")
	}
}

func TestModifiedLiveSharedHookFailsClosed(t *testing.T) {
	for _, vendor := range []string{"codex", "claude"} {
		for _, action := range []string{"install", "uninstall"} {
			t.Run(vendor+action, func(t *testing.T) {
				root, wt, c := setup(t)
				bin := filepath.Join(root, "rein")
				Install(c, bin, []string{vendor})
				rel := ".codex/hooks.json"
				if vendor == "claude" {
					rel = ".claude/settings.local.json"
				}
				path := filepath.Join(wt, rel)
				doc := mustJSON(t, path)
				entry := doc["hooks"].(map[string]any)["PreToolUse"].([]any)[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
				entry["command"] = entry["command"].(string) + " # local timeout note"
				writeJSON(path, doc)
				before, _ := os.ReadFile(path)
				generation := c.HooksGeneration
				var results []Result
				var err error
				if action == "install" {
					results, _, err = Install(c, bin, []string{vendor})
				} else {
					results, _, err = Uninstall(c, []string{vendor})
				}
				if err != nil || results[0].Err == nil {
					t.Fatalf("modified executable hook accepted: %+v %v", results, err)
				}
				after, _ := os.ReadFile(path)
				if string(before) != string(after) || c.HooksGeneration != generation || len(c.HooksInstalled) != 1 {
					t.Fatal("conflict changed bytes or contract")
				}
			})
		}
	}
}

func TestUninstallRemovesMovedSharedEvent(t *testing.T) {
	root, wt, c := setup(t)
	Install(c, filepath.Join(root, "rein"), []string{"codex"})
	path := filepath.Join(wt, ".codex/hooks.json")
	doc := mustJSON(t, path)
	events := doc["hooks"].(map[string]any)
	events["SessionStart"] = events["PreToolUse"]
	delete(events, "PreToolUse")
	writeJSON(path, doc)
	results, _, err := Uninstall(c, []string{"codex"})
	if err != nil || results[0].Err != nil {
		t.Fatalf("uninstall: %+v %v", results, err)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "--task w") {
		t.Fatalf("live moved hook survived: %s", b)
	}
}

func TestAgyMovedNamespaceFailsClosed(t *testing.T) {
	for _, action := range []string{"install", "uninstall"} {
		t.Run(action, func(t *testing.T) {
			root, wt, c := setup(t)
			bin := filepath.Join(root, "rein")
			Install(c, bin, []string{"agy"})
			path := filepath.Join(wt, ".agents/hooks.json")
			doc := mustJSON(t, path)
			doc["custom"] = doc["rein"]
			writeJSON(path, doc)
			before, _ := os.ReadFile(path)
			generation := c.HooksGeneration
			var results []Result
			if action == "install" {
				results, _, _ = Install(c, bin, []string{"agy"})
			} else {
				results, _, _ = Uninstall(c, []string{"agy"})
			}
			if results[0].Err == nil {
				t.Fatal("residual executable namespace accepted")
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) || c.HooksGeneration != generation || len(c.HooksInstalled) != 1 {
				t.Fatal("conflict changed state")
			}
		})
	}
}

func TestOpencodeGuardsNativeShellAndToolCallbacks(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture requires Unix")
	}
	root, wt, c := setup(t)
	bin := filepath.Join(root, "guard")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ncat > \"$REIN_TEST_EVENT\"\necho '[rein] denied' >&2\nexit 2\n"), 0755); err != nil {
		t.Fatal(err)
	}
	results, _, err := Install(c, bin, []string{"opencode"})
	if err != nil || results[0].Err != nil {
		t.Fatalf("install: %v %+v", err, results)
	}
	event := filepath.Join(root, "event.json")
	canon, err := filepath.EvalSymlinks(wt)
	if err != nil {
		t.Fatal(err)
	}
	js := `import {pathToFileURL} from 'node:url';
import {readFileSync} from 'node:fs';
const {default:p}=await import(pathToFileURL(process.argv[1]));
let tool,shell;
p.setup({tool:{hook:(n,f)=>{if(n!=="execute.before")throw Error(n);tool=f}},shell:{hook:(n,f)=>{if(n!=="create.before")throw Error(n);shell=f}}});
for(const [f,input] of [[tool,{tool:"shell",input:{command:"git push"}}],[shell,{command:"git push",cwd:"/untrusted"}]]) {
 let denied=false;
 try {await f(input)} catch(e) {denied=String(e).includes("[rein] denied")}
 if(!denied)throw Error("unguarded callback");
 const event=JSON.parse(readFileSync(process.env.REIN_TEST_EVENT,"utf8"));
 if(event.tool!=="shell"||event.input.command!=="git push"||event.cwd!==process.argv[2]||(f===shell&&event.input.workdir!=="/untrusted"))throw Error(JSON.stringify(event));
}
`
	cmd := exec.Command(node, "--input-type=module", "-e", js, filepath.Join(wt, ".opencode/plugins/rein/server.js"), canon)
	cmd.Env = append(os.Environ(), "REIN_TEST_EVENT="+event)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("native callbacks: %v %s", err, out)
	}
}
