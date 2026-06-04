---
name: code-critic
description: "Critically review code for bugs, quality issues, security vulnerabilities, performance problems, architecture concerns, and testing gaps. Use when: asked to review/fix code, find bugs, suggest improvements, audit quality, check security, recommend refactoring, or verify changes before commit."
argument-hint: "file path, change description, or area to focus on"
user-invocable: true
---

# Code Critic & Recommender

## What This Skill Does

Analyzes source code critically to identify issues across multiple dimensions and provides actionable recommendations. It combines static analysis thinking with best-practice knowledge to act as an automated code reviewer.

## When to Use

- **Code review**: "Review this PR/changes", "Check my code"
- **Bug hunting**: "Find bugs in this file", "Why is this broken?"
- **Quality audit**: "Is this idiomatic?", "How can I improve this?"
- **Security check**: "Are there vulnerabilities?", "Is this safe?"
- **Pre-commit check**: "Does this make sense?", "Verify my changes"
- **Refactoring advice**: "How should I restructure this?"

## Investigation Dimensions

Examine code across all of these axes, using the conversation and context to determine which to prioritize:

### 1. Bug Detection

- Logic errors and off-by-one mistakes
- Race conditions and concurrency bugs (goroutine leaks, deadlocks, unsafe shared state)
- nil pointer / null reference risks
- Resource leaks (file handles, network connections, goroutines)
- Incorrect error handling (swallowed errors, panics instead of errors)
- Edge cases (empty inputs, boundary values, type overflows)

### 2. Code Quality

- Duplicated or dead code (copy-paste errors, orphaned functions)
- Violations of DRY, KISS, YAGNI principles
- Unclear naming or inconsistent conventions
- Overly complex functions (high cyclomatic complexity)
- Missing comments on non-obvious logic
- Deep nesting that could be flattened

### 3. Security

- Injection vulnerabilities (SQL, command, template)
- Hardcoded secrets, tokens, or credentials
- Missing input validation or sanitization
- Insecure cryptographic practices
- Privilege escalation paths
- Exposure of sensitive data in logs or errors

### 4. Performance

- Inefficient algorithms (nested loops over large data)
- Unnecessary allocations or copies
- Missing caching opportunities
- Blocking calls in concurrent contexts
- Connection pool / resource exhaustion issues

### 5. Architecture

- Tight coupling and low cohesion
- Violations of layering (e.g., data access in presentation)
- Missing abstractions or over-engineering
- Improper separation of concerns
- Inconsistent error propagation patterns

### 6. Testing

- Missing tests for critical paths
- Tests that don't actually assert anything
- Brittle test patterns (timing-dependent, order-dependent)
- Mocks that overspecify implementation details
- Coverage gaps in edge cases or error paths

## Procedure

### Step 1: Understand Context

1. Read the file(s) involved in the request — get the full picture, not just the highlighted lines.
2. If the user references changes, run `git diff` or `git status` to see what's modified.
3. Identify what the code is supposed to do (from names, comments, or user description).

### Step 2: Analyze

1. Walk through each function/method systematically.
2. Trace control flow, especially error paths and edge cases.
3. Look for each dimension above — but focus on the most impactful issues first.
4. Check related files if the issue spans boundaries (configs, callers, interfaces).

### Step 3: Critique

For each issue found, report:

- **Severity**: critical / major / minor / suggestion
- **Location**: specific function or line
- **Problem**: what's wrong and why it matters
- **Evidence**: show the problematic code

### Step 4: Recommend

For each issue, provide:

- **Fix**: concrete code change or refactoring strategy
- **Rationale**: why this fix is correct
- **Alternative**: if multiple valid approaches exist

### Step 5: Verify

1. If fixes were applied, run `get_errors` to check for compilation/lint errors.
2. Run `git diff` to review the changes before presenting them.
3. Confirm the fix actually resolves the original issue (no regressions).

## Output Format

Use this structure when presenting findings:

```
## Review: <file or scope>

### 🔴 Critical
- **Issue**: description
- **Location**: `file.go:42`
- **Fix**: suggested change

### 🟡 Major
...

### 🔵 Suggestion
...

### ✅ Summary
- Issues found: N
- Critical: N, Major: N, Minor: N, Suggestions: N
- Overall assessment: brief verdict
```

## Key Principles

1. **Be specific** — cite exact line numbers and show code snippets.
2. **Be constructive** — every complaint must come with a recommendation.
3. **Prioritize** — surface critical bugs first, style nits last.
4. **Consider the context** — a pattern that's fine in a script may be wrong in production code.
5. **Verify before reporting** — check for false positives by understanding the full code path.
6. **Respect the codebase style** — don't recommend style changes that contradict existing conventions unless they're actively harmful.

## References

No additional references needed for this skill.
