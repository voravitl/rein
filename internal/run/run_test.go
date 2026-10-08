package run

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/voravitl/rein/internal/contract"
)

func git0(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newRepo makes a repo with one commit on branch main and returns its real root.
func newRepo(t *testing.T) string {
	t.Helper()
	root := contract.Real(t.TempDir())
	git0(t, root, "init", "-q", "-b", "main")
	write(t, root, "src/a.go", "package a\n")
	write(t, root, "docs/d.md", "d\n")
	git0(t, root, "add", ".")
	git0(t, root, "commit", "-q", "-m", "init")
	return root
}

func me() Owner { return Owner{SessionID: "sess-1", PID: os.Getpid()} }

func TestStartLifecycle(t *testing.T) {
	root := newRepo(t)
	m, err := Start(root, "sprint-1", &contract.Profile{ReadonlyAgents: []string{"Explore"}}, me())
	if err != nil {
		t.Fatal(err)
	}
	if m.Root != root || m.SessionID != "sess-1" || m.PID != os.Getpid() || m.StartTime == 0 || m.BaseRef != "main" || m.Schema != Schema {
		t.Fatalf("marker = %+v", m)
	}
	if strings.Join(m.CoordinatorWritable, ",") != strings.Join(DefaultWritable, ",") || len(m.CoordinatorTools) == 0 || m.ReadonlyAgents[0] != "Explore" {
		t.Fatalf("defaults not snapshotted: %+v", m)
	}
	if _, err := os.Stat(filepath.Join(root, ".git", MarkerFile)); err != nil {
		t.Fatalf("marker not under the git common dir: %v", err)
	}
	// a second start (from the main checkout or a linked worktree) is refused while the run is active
	wt := filepath.Join(contract.Real(t.TempDir()), "wt")
	git0(t, root, "worktree", "add", "-q", "-b", "w", wt)
	for _, repo := range []string{root, wt} {
		var act *ErrActive
		if _, err := Start(repo, "other", nil, me()); !errors.As(err, &act) || act.Run != "sprint-1" || !act.Alive {
			t.Fatalf("second start from %s = %v", repo, err)
		}
	}
	// allow, then resume with a new session id (only the owner pid may)
	if _, err := Allow(wt, "task-a", "user said so"); err != nil {
		t.Fatal(err)
	}
	if _, err := Allow(root, "task-a", "again"); err != nil { // no duplicates
		t.Fatal(err)
	}
	if _, err := Allow(root, "t", " "); err == nil {
		t.Fatal("allow without a reason must fail")
	}
	if _, err := Resume(root, "sess-2", os.Getpid()+1); err == nil {
		t.Fatal("resume from another pid must fail")
	}
	if _, err := Resume(wt, "sess-2", os.Getpid()); err != nil {
		t.Fatal(err)
	}
	loc, _ := Locate(root)
	got, err := Load(loc.Marker)
	if err != nil || got.SessionID != "sess-2" || len(got.Allowed) != 1 || !got.Allows("task", "task-a") || got.Allows("task", "task-b") {
		t.Fatalf("after allow+resume: %+v %v", got, err)
	}
	// clean end closes (no commits since start), records history, removes the marker
	res, err := End(root, false, "", AuditOptions{})
	if err != nil || !res.Closed {
		t.Fatalf("end = %+v %v", res, err)
	}
	if _, err := os.Stat(loc.Marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("marker still there")
	}
	if b, _ := os.ReadFile(filepath.Join(root, ".git", "rein-run-history.jsonl")); !strings.Contains(string(b), `"run":"sprint-1"`) {
		t.Fatalf("history = %q", b)
	}
	if _, err := Start(root, "next", nil, me()); err != nil { // a new run may start after the end
		t.Fatal(err)
	}
}

func TestStartRefusals(t *testing.T) {
	root := newRepo(t)
	if _, err := Start(root, "bad name", nil, me()); err == nil {
		t.Fatal("name with a space")
	}
	if _, err := Start(root, "ok", nil, Owner{PID: os.Getpid()}); err == nil {
		t.Fatal("owner without a session")
	}
	if _, err := Start(root, "ok", nil, Owner{SessionID: "s", PID: 1 << 30}); err == nil {
		t.Fatal("owner pid that does not run")
	}
	if _, err := Start(t.TempDir(), "ok", nil, me()); err == nil {
		t.Fatal("not a git repo")
	}
}

func TestLocateMainAndLinked(t *testing.T) {
	root := newRepo(t)
	wt := filepath.Join(contract.Real(t.TempDir()), "wt")
	git0(t, root, "worktree", "add", "-q", "-b", "w", wt)
	write(t, wt, "pkg/deep/x.txt", "x")
	main, err := Locate(filepath.Join(root, "src"))
	if err != nil || main.Linked || main.Top != root || main.Common != filepath.Join(root, ".git") {
		t.Fatalf("main = %+v %v", main, err)
	}
	link, err := Locate(filepath.Join(wt, "pkg", "deep"))
	if err != nil || !link.Linked || link.Top != wt || real(link.Common) != real(main.Common) {
		t.Fatalf("linked = %+v %v", link, err)
	}
	if link.Marker != main.Marker && real(filepath.Dir(link.Marker)) != real(filepath.Dir(main.Marker)) {
		t.Fatalf("marker paths differ: %s vs %s", link.Marker, main.Marker)
	}
}

// fakeRepo lays out a main checkout and a linked worktree by hand: Find must work without running git.
func fakeRepo(t *testing.T) (root, wt string) {
	t.Helper()
	base := contract.Real(t.TempDir())
	root, wt = filepath.Join(base, "main"), filepath.Join(base, "wt")
	gd := filepath.Join(root, ".git", "worktrees", "wt")
	write(t, gd, "commondir", "../..\n")
	write(t, gd, "gitdir", filepath.Join(wt, ".git")+"\n")
	write(t, wt, ".git", "gitdir: "+gd+"\n")
	write(t, root, "src/a.go", "x")
	write(t, wt, "pkg/b.go", "x")
	return root, wt
}

func putMarker(t *testing.T, common string) string {
	t.Helper()
	p := MarkerPath(common)
	if err := writeMarker(p, &Marker{Schema: Schema, Run: "r", Root: "/x", SessionID: "s", PID: os.Getpid()}, false); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFindWithoutGit(t *testing.T) {
	root, wt := fakeRepo(t)
	if _, ok := Find(filepath.Join(wt, "pkg")); ok {
		t.Fatal("found a marker that does not exist")
	}
	mp := putMarker(t, filepath.Join(root, ".git"))
	for _, dir := range []string{root, filepath.Join(root, "src"), wt, filepath.Join(wt, "pkg")} {
		loc, ok := Find(dir)
		if !ok || loc.Marker != mp {
			t.Fatalf("Find(%s) = %+v %v", dir, loc, ok)
		}
	}
	if loc, _ := Find(wt); !loc.Linked || loc.Top != wt {
		t.Fatalf("linked worktree not recognised: %+v", loc)
	}
	if loc, _ := Find(root); loc.Linked {
		t.Fatalf("main checkout recognised as linked: %+v", loc)
	}
}

func TestFindRelativeGitdirAndSubmodule(t *testing.T) {
	root, wt := fakeRepo(t)
	mp := putMarker(t, filepath.Join(root, ".git"))
	// the worktree's .git file with a gitdir relative to the .git file's directory
	rel, _ := filepath.Rel(wt, filepath.Join(root, ".git", "worktrees", "wt"))
	write(t, wt, ".git", "gitdir: "+rel+"\n")
	if loc, ok := Find(wt); !ok || loc.Marker != mp || !loc.Linked {
		t.Fatalf("relative gitdir: %+v %v", loc, ok)
	}
	// a submodule: .git file -> <main>/.git/modules/sm (no commondir, not under worktrees/): no marker of its own,
	// so the walk continues to the superproject, whose run applies
	sm := filepath.Join(root, "vendor", "sm")
	write(t, filepath.Join(root, ".git", "modules", "sm"), "HEAD", "ref: refs/heads/main\n")
	write(t, sm, ".git", "gitdir: ../../.git/modules/sm\n")
	write(t, sm, "f.go", "x")
	if loc, ok := Find(sm); !ok || loc.Marker != mp || loc.Top != root {
		t.Fatalf("submodule: %+v %v", loc, ok)
	}
	// a gitdir that has a commondir but is NOT under <common>/worktrees/ is not a linked worktree
	odd := filepath.Join(root, ".git", "elsewhere", "odd")
	write(t, odd, "commondir", "../..\n")
	fake := filepath.Join(t.TempDir(), "fake")
	write(t, fake, ".git", "gitdir: "+odd+"\n")
	if loc, ok := Find(fake); ok {
		t.Fatalf("a fake linked worktree must not find the run: %+v", loc)
	}
}

func TestSeparateGitDirAndRedirect(t *testing.T) {
	base := contract.Real(t.TempDir())
	gd, root := filepath.Join(base, "store.git"), filepath.Join(base, "checkout")
	write(t, gd, "HEAD", "ref: refs/heads/main\n")
	write(t, root, ".git", "gitdir: "+gd+"\n")
	mp := putMarker(t, gd)
	if loc, ok := Find(root); !ok || loc.Marker != mp {
		t.Fatalf("separate git dir: %+v %v", loc, ok)
	}
	// REIN_RUN_DIR redirects the marker; a repo that is not the marker's root does not belong to it
	rd := t.TempDir()
	t.Setenv("REIN_RUN_DIR", rd)
	if MarkerPath(gd) != filepath.Join(rd, MarkerFile) {
		t.Fatalf("redirect = %s", MarkerPath(gd))
	}
	if err := writeMarker(MarkerPath(gd), &Marker{Schema: Schema, Run: "r", Root: root, SessionID: "s", PID: 1}, false); err != nil {
		t.Fatal(err)
	}
	loc, ok := Find(root)
	m, _ := Load(loc.Marker)
	if !ok || !loc.Belongs(m) {
		t.Fatalf("redirected marker not found/owned: %+v %v", loc, ok)
	}
	other, _ := fakeRepo(t)
	loc2, ok2 := Find(other)
	if !ok2 || loc2.Belongs(m) {
		t.Fatalf("an unrelated repo must not belong to the redirected run: %+v %v", loc2, ok2)
	}
}

func TestGitEnvRedirect(t *testing.T) {
	root, _ := fakeRepo(t)
	mp := putMarker(t, filepath.Join(root, ".git"))
	t.Setenv("GIT_DIR", filepath.Join(root, ".git"))
	if loc, ok := Find(root); !ok || !loc.EnvRedirect || loc.Marker != mp {
		t.Fatalf("cwd inside the repo with GIT_DIR set: %+v %v", loc, ok)
	}
	if loc, ok := Find(t.TempDir()); !ok || !loc.EnvRedirect { // cwd elsewhere, GIT_DIR points at the repo
		t.Fatalf("cwd outside with GIT_DIR set: %+v %v", loc, ok)
	}
}

func TestCorruptAndUnknownSchema(t *testing.T) {
	root := newRepo(t)
	p := filepath.Join(root, ".git", MarkerFile)
	for name, body := range map[string]string{
		"garbage":        "{not json",
		"empty":          "",
		"unknown schema": `{"schema": 99, "run": "r", "root": "/x", "session_id": "s", "pid": 1}`,
		"no schema":      `{"run": "r", "root": "/x", "session_id": "s", "pid": 1}`,
		"missing fields": `{"schema": 1, "run": "r"}`,
	} {
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		var bad *BadMarker
		if _, err := Load(p); !errors.As(err, &bad) {
			t.Errorf("%s: Load = %v, want *BadMarker", name, err)
		}
		if _, err := Start(root, "x", nil, me()); !errors.As(err, &bad) { // never overwritten by start
			t.Errorf("%s: Start = %v, want *BadMarker", name, err)
		}
	}
	if _, err := End(root, false, "", AuditOptions{}); err == nil {
		t.Fatal("a clean end of a corrupt marker must fail")
	}
	if _, err := End(root, true, "", AuditOptions{}); err == nil {
		t.Fatal("abandon needs a reason")
	}
	if res, err := End(root, true, "corrupt", AuditOptions{}); err != nil || !res.Closed {
		t.Fatalf("abandon of a corrupt marker = %+v %v", res, err)
	}
	if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("marker still there")
	}
	if _, err := Load(p); !errors.Is(err, ErrNoRun) {
		t.Fatalf("Load of a missing file = %v", err)
	}
}

func TestAtomicWrite(t *testing.T) {
	root := newRepo(t)
	if _, err := Start(root, "r", nil, me()); err != nil {
		t.Fatal(err)
	}
	loc, _ := Locate(root)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	bad := make(chan error, 1)
	wg.Add(1)
	go func() { // a reader (the hook) must never see a half-written marker
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := Load(loc.Marker); err != nil {
				select {
				case bad <- err:
				default:
				}
				return
			}
		}
	}()
	for i := 0; i < 200; i++ {
		if _, err := Allow(root, "task-"+strings.Repeat("a", i%20+1), "r"); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
	select {
	case err := <-bad:
		t.Fatalf("reader saw %v", err)
	default:
	}
	ents, _ := os.ReadDir(filepath.Join(root, ".git"))
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

func TestOwnerAlive(t *testing.T) {
	st, err := ProcStart(os.Getpid())
	if err != nil || st == 0 {
		t.Fatalf("ProcStart(self) = %d %v", st, err)
	}
	if !(&Marker{PID: os.Getpid(), StartTime: st}).OwnerAlive() {
		t.Fatal("self with the right start time must be alive")
	}
	if (&Marker{PID: os.Getpid(), StartTime: st + 12345}).OwnerAlive() {
		t.Fatal("a reused pid (other start time) must be dead")
	}
	if (&Marker{PID: 1 << 30, StartTime: st}).OwnerAlive() {
		t.Fatal("a pid that does not run must be dead")
	}
	if (&Marker{PID: 0}).OwnerAlive() {
		t.Fatal("no pid must be dead")
	}
	cmd := exec.Command(os.Args[0], "-test.run=NoSuchTestXYZ")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := ProcStart(cmd.Process.Pid); !errors.Is(err, ErrNoProc) {
		t.Fatalf("exited child: %v", err)
	}
}

func commitFile(t *testing.T, root, rel, msg string) string {
	t.Helper()
	write(t, root, rel, msg+"\n")
	git0(t, root, "add", rel)
	git0(t, root, "commit", "-q", "-m", msg)
	return git0(t, root, "rev-parse", "HEAD")
}

func TestAudit(t *testing.T) {
	root := newRepo(t)
	if _, err := Start(root, "r", nil, me()); err != nil {
		t.Fatal(err)
	}
	if f, err := Audit(root, AuditOptions{}); err != nil || len(f) != 0 {
		t.Fatalf("no commits yet: %v %v", f, err)
	}
	commitFile(t, root, "docs/new.md", "docs only")
	commitFile(t, root, "README.md", "readme")
	if f, err := Audit(root, AuditOptions{}); err != nil || len(f) != 0 {
		t.Fatalf("writable-only commits must pass: %v %v", f, err)
	}
	// a worker's feature branch, merged with a merge commit: the worker commit is reachable from the pin, the
	// merge commit itself is skipped (--no-merges)
	git0(t, root, "checkout", "-q", "-b", "feat")
	work := commitFile(t, root, "src/feat.go", "worker feature")
	git0(t, root, "checkout", "-q", "main")
	git0(t, root, "merge", "-q", "--no-ff", "-m", "merge feat", "feat")
	f, err := Audit(root, AuditOptions{})
	if err != nil || len(f) != 1 || f[0].Kind != "COORDINATOR_DRIFT" || f[0].Commit != work || !strings.Contains(f[0].Detail, "src/feat.go") {
		t.Fatalf("unpinned worker commit: %+v %v", f, err)
	}
	if f, err := Audit(root, AuditOptions{Pinned: []string{work}}); err != nil || len(f) != 0 {
		t.Fatalf("pinned (reachable) commit must pass: %+v %v", f, err)
	}
	// a coordinator edit of code, direct on main
	own := commitFile(t, root, "src/a.go", "coordinator edits code")
	f, _ = Audit(root, AuditOptions{Pinned: []string{work}})
	if len(f) != 1 || f[0].Commit != own {
		t.Fatalf("direct edit: %+v", f)
	}
	// ... covered by a recorded exception (a unique prefix of at least 7 characters)
	if _, err := AllowCommit(root, own[:10], "typo fix the user approved"); err != nil {
		t.Fatal(err)
	}
	if f, _ = Audit(root, AuditOptions{Pinned: []string{work}}); len(f) != 0 {
		t.Fatalf("exception not honoured: %+v", f)
	}
	if _, err := AllowCommit(root, "abc", "short"); err == nil {
		t.Fatal("short sha")
	}
	if _, err := Audit(root, AuditOptions{Pinned: []string{"no-such-ref"}}); err == nil {
		t.Fatal("unknown pinned ref must fail, not pass")
	}
	// a clean end with drift stays open; abandon closes with the reason
	git0(t, root, "checkout", "-q", "-b", "scratch", "main")
	commitFile(t, root, "src/z.go", "unpinned") // on another branch: not in start..main
	git0(t, root, "checkout", "-q", "main")
	m2 := commitFile(t, root, "src/y.go", "another coordinator edit")
	res, err := End(root, false, "", AuditOptions{Pinned: []string{work}})
	if err != nil || res.Closed || len(res.Findings) != 1 || res.Findings[0].Commit != m2 {
		t.Fatalf("end with drift = %+v %v", res, err)
	}
	res, err = End(root, true, "user ruled it fine", AuditOptions{})
	if err != nil || !res.Closed {
		t.Fatalf("abandon = %+v %v", res, err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, ".git", "rein-run-history.jsonl")); !strings.Contains(string(b), `"abandoned":true`) || !strings.Contains(string(b), "user ruled it fine") {
		t.Fatalf("history = %q", b)
	}
}

func TestAuditPatchIDAfterRebase(t *testing.T) {
	root := newRepo(t)
	if _, err := Start(root, "r", nil, me()); err != nil {
		t.Fatal(err)
	}
	git0(t, root, "checkout", "-q", "-b", "feat")
	pin := commitFile(t, root, "src/feat.go", "worker feature")
	git0(t, root, "checkout", "-q", "main")
	commitFile(t, root, "docs/moved.md", "main moves on") // else the cherry-pick below reproduces the same sha
	git0(t, root, "cherry-pick", pin)                     // same patch, new sha: what a rebase-merge produces
	if head := git0(t, root, "rev-parse", "HEAD"); head == pin {
		t.Fatal("the cherry-pick kept the pinned sha; the test cannot tell patch-id from reachability")
	}
	if f, _ := Audit(root, AuditOptions{}); len(f) != 1 {
		t.Fatalf("without the pin the cherry-picked commit is drift: %+v", f)
	}
	if f, err := Audit(root, AuditOptions{Pinned: []string{pin}}); err != nil || len(f) != 0 {
		t.Fatalf("patch-id equal commit must pass: %+v %v", f, err)
	}
}

func TestAgentBinding(t *testing.T) {
	mp := filepath.Join(t.TempDir(), MarkerFile)
	if err := RecordPending(mp, "s", "task-a"); err != nil {
		t.Fatal(err)
	}
	if err := RecordPending(mp, "s", ""); !errors.Is(err, ErrAmbiguous) { // a read-only start next to a task start
		t.Fatalf("second pending next to a task = %v", err)
	}
	if err := RecordPending(mp, "s", "task-b"); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("two tasks pending = %v", err)
	}
	task, bound, err := BindStart(mp, "agent-1")
	if err != nil || !bound || task != "task-a" {
		t.Fatalf("bind = %q %v %v", task, bound, err)
	}
	if got, ok := BoundTask(mp, "agent-1"); !ok || got != "task-a" {
		t.Fatalf("BoundTask = %q %v", got, ok)
	}
	if _, bound, _ := BindStart(mp, "agent-2"); bound {
		t.Fatal("nothing pending: nothing to bind")
	}
	if _, ok := BoundTask(mp, "agent-2"); ok {
		t.Fatal("agent-2 must be unbound")
	}
	// two read-only starts may overlap, and bind first-in first-out
	for i := 0; i < 2; i++ {
		if err := RecordPending(mp, "s", ""); err != nil {
			t.Fatalf("read-only pending %d = %v", i, err)
		}
	}
	for _, id := range []string{"a3", "a4"} {
		if task, bound, _ := BindStart(mp, id); !bound || task != "" {
			t.Fatalf("read-only bind %s = %q %v", id, task, bound)
		}
	}
	if got, ok := BoundTask(mp, "a3"); !ok || got != "" {
		t.Fatalf("read-only subagent: %q %v", got, ok)
	}
	ClearAgents(mp)
	if _, ok := BoundTask(mp, "agent-1"); ok {
		t.Fatal("binding survived ClearAgents")
	}
}

func TestPendingExpires(t *testing.T) {
	mp := filepath.Join(t.TempDir(), MarkerFile)
	if err := RecordPending(mp, "s", "task-a"); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(AgentsDir(mp))
	old := `{"session":"s","task":"task-a","at":1}`
	if err := os.WriteFile(filepath.Join(AgentsDir(mp), ents[0].Name()), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RecordPending(mp, "s", "task-b"); err != nil { // the lost start no longer blocks
		t.Fatalf("stale pending still blocks: %v", err)
	}
}

// TestRunDirHelper verifies RunDir fallback from PIPELINE_RUNS to ~/.cache (ADR 0002 B4).
func TestRunDirHelper(t *testing.T) {
	// Test with PIPELINE_RUNS set
	t.Setenv("PIPELINE_RUNS", "/custom/runs")
	got := RunDir("test-run")
	want := "/custom/runs/test-run"
	if got != want {
		t.Errorf("RunDir with PIPELINE_RUNS = %q, want %q", got, want)
	}

	// Test fallback to ~/.cache
	t.Setenv("PIPELINE_RUNS", "")
	got = RunDir("test-run")
	home, _ := os.UserHomeDir()
	want = filepath.Join(home, ".cache", "worktree-pipeline", "runs", "test-run")
	if got != want {
		t.Errorf("RunDir fallback = %q, want %q", got, want)
	}
}

// TestSetRunDir verifies SetRunDir updates marker (ADR 0002 B4).
func TestSetRunDir(t *testing.T) {
	root := newRepo(t)
	_, err := Start(root, "r1", &contract.Profile{}, me())
	if err != nil {
		t.Fatal(err)
	}
	loc, _ := Locate(root)
	m, _ := Load(loc.Marker)
	if m.RunDir != "" {
		t.Errorf("fresh marker has RunDir = %q, want empty", m.RunDir)
	}

	// Set RunDir
	runDir := filepath.Join(t.TempDir(), "test-run")
	if err := SetRunDir(loc.Marker, runDir); err != nil {
		t.Fatalf("SetRunDir: %v", err)
	}

	// Verify it was set
	m, err = Load(loc.Marker)
	if err != nil {
		t.Fatal(err)
	}
	if m.RunDir != runDir {
		t.Errorf("after SetRunDir: marker.RunDir = %q, want %q", m.RunDir, runDir)
	}

	// Verify idempotency
	if err := SetRunDir(loc.Marker, "/different/path"); err != nil {
		t.Fatal(err)
	}
	m, _ = Load(loc.Marker)
	if m.RunDir != runDir {
		t.Errorf("SetRunDir changed existing RunDir: got %q, want %q", m.RunDir, runDir)
	}
}
