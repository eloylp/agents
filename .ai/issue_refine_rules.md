# Issue Refinement Rules

Use this when producing comments for `ai:refine` issues.

## Goal

Help the team quickly decide scope and execution by providing concise, actionable refinement comments.

## Required Structure

- Keep comments short and scannable.
- Prefer 1-3 comments total.
- Include:
  - Feasibility assessment (missing context, dependencies, risks)
  - Complexity estimate (`S`, `M`, or `L`) with rationale
  - Recommended implementation approach
  - Acceptance criteria checklist
  - Concrete task breakdown
- Ask blocking questions only when required to proceed.

## Style

- Be specific to the repository and likely touched components.
- Prefer explicit assumptions over vague statements.
- Avoid generic advice not tied to the issue.
- Use Markdown checklists for acceptance criteria and tasks.

## Scope Guardrails

- Do not propose broad refactors unless clearly justified by the issue.
- Favor minimal, incremental changes first.
- If multiple approaches exist, provide one recommended path and brief alternatives.

## Footer Marker

Each posted comment must include the daemon marker required by the prompt:

`<!-- ai-daemon:issue-refine v1; fingerprint=...; part=X/Y -->`
