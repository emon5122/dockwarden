---
name: polish-loop
description: "Iteratively review and fix code until the code-critic is satisfied. Use when: asked to fix code until clean, polish changes, self-heal code, iterate on review feedback, keep fixing until no issues remain, or run the critic in a loop."
argument-hint: "files, changes, or area to polish"
user-invocable: true
---

# Polish Loop

## What This Skill Does

Runs the **code-critic** skill in an iterative loop: review → fix → re-review → repeat until the critic finds no more actionable issues (or the maximum round limit is reached). This automates the self-healing process demonstrated in the DockWarden health watcher review.

## When to Use

- **Self-healing code**: "Fix this code until it's clean", "Polish my changes"
- **Iterative review**: "Keep fixing until the critic is happy"
- **Pre-commit polish**: "Review and fix everything before I commit"
- **PR preparation**: "Get this code ready for review"
- **After refactoring**: "Check if my changes are solid and fix any issues"
- **Quality gate**: "Run the critic and fix all critical/major issues"

Trigger phrases: "fix until clean", "polish this", "keep fixing", "iterate until done", "loop the critic", "self-heal".

## Procedure

### Step 1: Understand Context

1. If specific files are mentioned, read them in full.
2. If no files are specified, run `git diff` and `git status` to identify what's changed.
3. Identify what the code is supposed to do from names, comments, or user description.

### Step 2: Run the Critic

1. Load and follow the [code-critic skill](../code-critic/SKILL.md).
2. Perform a full review across all 6 dimensions (bugs, quality, security, performance, architecture, testing).
3. Present the findings using the code-critic output format.

### Step 3: Apply Fixes

For each issue found by the critic (in priority order):

1. **Critical first** — fix all 🔴 issues before anything else.
2. **Then Major** — fix all 🟡 issues next.
3. **Then Minor/Suggestions** — fix 🔵 suggestions if time permits and they add meaningful value.

For each fix:

- Use `get_errors` after each edit to confirm the code still compiles.
- Use `git diff` periodically to verify changes are correct.

### Step 4: Re-verify

After applying all fixes from this round:

1. Run `get_errors` across the affected files — confirm zero compilation/lint errors.
2. Run `git diff` to review the full set of changes.

### Step 5: Loop Back or Exit

1. **Re-run the critic** on the fixed code.
2. **Evaluate remaining issues:**
    - If **zero issues found** → ✅ Done. Present a summary.
    - If **only suggestions remain** and they're cosmetic → ✅ Acceptable. Present a summary.
    - If **critical or major issues remain** → 🔄 Loop back to Step 3.
    - If **max rounds (10) reached** → ⚠️ Stop. Present a summary of what was fixed and what remains.

### Step 6: Present Summary

Report:

```
## Polish Loop Summary
- **Rounds completed**: N
- **Total issues fixed**: N (Critical: N, Major: N, Minor: N)
- **Remaining issues**: N
- **Files modified**: file1.go, file2.go
- **Status**: ✅ Clean / ⚠️ Partial (reason)
```

## Decision Points

| Condition                          | Action                              |
| ---------------------------------- | ----------------------------------- |
| Round 1, all fixes applied         | Re-run critic                       |
| Critic finds 0 issues              | ✅ Stop — done                      |
| Critic finds only suggestions      | ✅ Stop — acceptable                |
| Critic finds critical/major issues | 🔄 Loop — fix them                  |
| Max rounds (10) reached            | ⚠️ Stop — report partial            |
| Errors after applying fix          | Fix errors before re-running critic |

## Key Principles

1. **Prioritize ruthlessly** — fix critical bugs first. Don't waste rounds on cosmetic nits when there are real bugs.
2. **Verify each fix** — run `get_errors` after every edit. A broken build wastes the round.
3. **Don't chase perfection** — suggestions that would take longer than the fix is worth are acceptable to leave.
4. **Respect the codebase** — don't make style-only changes that contradict existing conventions unless they're actively harmful.
5. **Report honestly** — if something can't be fixed without a major refactor, note it and move on.

## References

This skill depends on the [code-critic skill](../code-critic/SKILL.md) — it must also be available in the workspace.
