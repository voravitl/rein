---
name: no-comments
description: Review comments and suppressions for redundant prose or missing structural guarantees, then apply only scoped and verified accepted changes.
---

Read [Runtime guide](../../references/runtime.md) before executing this skill.

# No comments

Use the caller's named files or diff. Otherwise inspect the diff against the actual base branch, including working-tree changes. A comment review is not authorization to remove behavior or bypass guards.

1. Ask an advertised `crux-comment-reviewer`, or native `code-reviewer` with the full `../../agents/comment-reviewer.md` prompt path, for a read-only report. Include scope, exact revision, and evidence requirements. It proposes changes; it does not edit files or spawn children.
2. Inspect every proposal against surrounding code, tests, and history. Reject scope escapes, application-code edits, and unsupported claims. A named safety, security, compatibility, licensing, public API, or business constraint remains until proven obsolete or replaced by a verified structural guarantee. Ambiguity is an open finding, not a reason to delete. Use `how` or `why` when the code cannot settle intent.
3. The parent applies accepted low-risk comment cleanup within the user's authorized scope. Behavioral or architectural fixes require their own concrete scope and regression proof; review output alone does not authorize widening the task. Use the architect skill only when an accepted fix actually needs a new shape.
4. Offer the smallest type, runtime validation, regression test, or lint for a genuine constraint that prose alone cannot enforce. Implement it only when the user's authorization covers that change. Keep the constraint comment until the replacement is verified. Without authorization or proof, report the proposal open and preserve the comment and guard.
5. Run the relevant existing checks for any accepted edits, review the final diff, and report proposed/accepted/rejected changes, deletions, preserved constraints, encodings verified, and open work. Do not call an unverified encoding complete.

If delegation is forbidden or unavailable, perform the same bounded review sequentially and state that there was no independent reviewer. Never invent reviewer identities or cross-model evidence.
