#!/usr/bin/env python3
"""Remove worktrees whose work is finished: clean AND (HEAD is in origin/main OR every commit is
patch-equivalent to one in origin/main). Deletes the local branch too. Never the main checkout.

usage: cleanup_worktrees.py <main checkout> --keep <running tasks a,b | none> [--root <dir with worker worktrees>]
                             [--ignore <untracked paths that never block removal, e.g. frontend/node_modules>] [--dry-run]
       --keep is required: a just-started worker has a clean tree at origin/main and would look merged
       [--prune-images <repo>]   e.g. myapp-tests: removes every <repo>:* tag except :latest and images in use;
                                 run it when no worker is between its build and its test run
"""
import argparse, os, subprocess

def git(*args, cwd):
    return subprocess.run(["git", *args], cwd=cwd, capture_output=True, text=True)

ap = argparse.ArgumentParser(); ap.add_argument("main"); ap.add_argument("--root"); ap.add_argument("--keep", required=True, help="comma list of worktree names of RUNNING tasks, or 'none'")
ap.add_argument("--dry-run", action="store_true"); ap.add_argument("--prune-images")
ap.add_argument("--ignore", default="", help="comma list of UNTRACKED paths (repo-relative paths cover everything below them; a bare name like the profile's local_artifacts matches only a file or symlink of that name; a root-level directory is ./name) that do not count as uncommitted work")
a = ap.parse_args(); main = os.path.realpath(a.main); keep = {k for k in a.keep.split(",") if k and k != "none"}
ignore = {i.strip().rstrip("/") for i in a.ignore.split(",") if i.strip().rstrip("/") not in ("", ".", "./")}
TARGET = "origin/" + os.environ.get("PIPELINE_TARGET_BRANCH", "main")
if git("fetch", "origin", "--quiet", cwd=main).returncode != 0: raise SystemExit("[cleanup] git fetch origin failed (VPN/DNS?); nothing removed")
failed = []
lines = git("worktree", "list", "--porcelain", cwd=main).stdout.split("\n\n")
for block in lines:
    f = dict(l.split(" ", 1) if " " in l else (l, "") for l in block.strip().splitlines() if l)
    path = f.get("worktree"); branch = f.get("branch", "").replace("refs/heads/", "")
    if not path or os.path.realpath(path) == main: continue
    if a.root:
        r, p = os.path.realpath(a.root), os.path.realpath(path)
        if os.path.commonpath([r, p]) != r or p == r: continue   # component-aware: 'app' does not contain 'app-old'
    name = os.path.basename(path)
    if name in keep: print("keep   ", name, "(--keep)"); continue
    # Fail closed: any git error keeps the worktree (an empty stdout from a failed command must never read as "clean").
    # --untracked-files=all overrides a status.showUntrackedFiles=no config that would hide untracked work.
    st = git("status", "--porcelain", "-z", "--untracked-files=all", cwd=path)
    if st.returncode != 0: print("keep   ", name, "(git status failed:", st.stderr.strip()[:80] + ")"); continue
    # Only untracked paths under an --ignore entry (the entry itself or anything below it) are skipped; any tracked
    # change or other untracked file keeps the worktree.
    def ignorable(entry):
        if not entry.startswith("?? "): return False
        p = entry[3:].rstrip("/"); parts = p.split("/")
        # "frontend/node_modules" = that path and everything below it; a bare name ("tsconfig.tsbuildinfo", or
        # "node_modules" as a symlink) matches only a path whose LAST part has that name, so it can never hide work
        # inside a directory. Give artifact directories as repo-relative paths.
        # Write a root-level artifact directory as "./name".
        def hit(i):
            if "/" not in i: return parts[-1] == i
            i = i[2:] if i.startswith("./") else i
            return p == i or p.startswith(i + "/")
        return any(hit(i) for i in ignore)
    dirty = [e for e in st.stdout.split("\0") if e and not ignorable(e)]
    if dirty: print("keep   ", name, f"({len(dirty)} uncommitted changes)"); continue
    hd = git("rev-parse", "--verify", "HEAD", cwd=path); head = hd.stdout.strip()
    if hd.returncode != 0 or not head: print("keep   ", name, "(cannot resolve HEAD)"); continue
    anc = git("merge-base", "--is-ancestor", head, TARGET, cwd=path)
    if anc.returncode not in (0, 1): print("keep   ", name, "(merge-base failed:", anc.stderr.strip()[:80] + ")"); continue
    merged = anc.returncode == 0
    if not merged:
        mg = git("rev-list", "--merges", f"{TARGET}..{head}", cwd=path)
        if mg.returncode != 0 or mg.stdout.strip(): print("keep   ", name, "(unmerged merge commits: git cherry cannot judge them)"); continue
    ch = git("cherry", TARGET, head, cwd=path)
    if ch.returncode != 0: print("keep   ", name, "(git cherry failed:", ch.stderr.strip()[:80] + ")"); continue
    equivalent = "+" not in ch.stdout.split()
    if not (merged or equivalent): print("keep   ", name, f"(has commits not in {TARGET})"); continue
    print("remove ", name, "merged" if merged else "patch-equivalent", branch)
    if a.dry_run: continue
    try:
        r = subprocess.run(["orca", "worktree", "rm", "--worktree", f"path:{path}", "--force", "--json"], capture_output=True, text=True)
        orca_ok = '"ok": true' in r.stdout
    except FileNotFoundError:
        orca_ok = False
    if not orca_ok: git("worktree", "remove", "--force", path, cwd=main)
    if os.path.exists(path): print("FAILED ", name, "(worktree still on disk; branch kept)"); failed.append(name); continue
    if branch and git("branch", "-D", branch, cwd=main).returncode != 0: print("FAILED ", name, f"(branch {branch} not deleted)"); failed.append(branch)
if not a.dry_run and git("worktree", "prune", cwd=main).returncode != 0: failed.append("worktree prune")
if a.prune_images:
    im = subprocess.run(["docker", "images", "--format", "{{.Repository}}:{{.Tag}}", a.prune_images], capture_output=True, text=True)
    ps = subprocess.run(["docker", "ps", "--format", "{{.Image}}"], capture_output=True, text=True)
    if im.returncode != 0 or ps.returncode != 0:
        failed.append("docker listing (images or ps) failed; no image removed"); im = None
    tags = im.stdout.split() if im else []
    busy = set(ps.stdout.split())
    old = [t for t in tags if not t.endswith(":latest") and t not in busy]  # never an image a running container uses
    print("images ", len(old), "to remove", "(dry-run)" if a.dry_run else "")
    if old and not a.dry_run and subprocess.run(["docker", "rmi", *old], capture_output=True).returncode != 0: failed.append("docker rmi")
if failed: raise SystemExit(f"[cleanup] FAILED: {', '.join(failed)}")
