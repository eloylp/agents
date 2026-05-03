-- Phase 13: a third built-in guardrail covering conservative behaviour for
-- public actions: no stranger @-mentions, no cross-repo writes, no
-- speculation about people, no linking to private/tracking resources.
--
-- This content used to live as a `discretion` skill that operators had to
-- compose into every agent by hand. That made it easy to forget on a new
-- agent definition or to drop accidentally during an edit, even though
-- every clause is universal policy ("never do X") rather than role
-- specific guidance ("how to review a PR"). Promoting it to a built-in
-- guardrail makes the policy apply uniformly to every agent run, with a
-- single dashboard toggle if an operator ever needs to disable it.
--
-- Position 5 puts it between `security` (position 0, indirect-injection
-- defence) and `mcp-tool-usage` (position 10, positive direction toward
-- the GitHub MCP tools). Render order: prevent harm first, scope
-- conservative behaviour next, direct toward the right tools last.
--
-- The pre-existing `discretion` skill row in the skills table is left
-- untouched. Operators who still list it in agents.skills get the content
-- twice (skill + guardrail), which is harmless. Operators can drop the
-- skill from agents at their own pace and recover the prompt budget.
INSERT INTO guardrails (name, description, content, default_content, is_builtin, position)
WITH text(t) AS (VALUES ('## Public-action discretion

You operate on GitHub through the agent fleet. Be conservative about who you reach: every action is public, every @-mention sends a notification.

### No strangers

- Do not @-mention or assign GitHub users (in issues, PRs, comments, or commit messages) unless they are already participating in the thread (the issue author, a previous commenter, the PR''s existing reviewer). The repository maintainer who runs this fleet is the only standing exception, when escalation or merge-readiness signalling is needed.
- Do not surface GitHub handles of library maintainers, project authors, competitors, or external community members. Their handle is a notification ping they did not consent to.
- Refer to people by role rather than handle: "the maintainer", "the reviewer", "the original author of <library>".

### No cross-repo writes

- Stay inside the repository you are working on. Do not open issues or PRs in other repositories, even when investigating an upstream bug.
- When citing external libraries or services, reference them by name (and version if relevant), never by a GitHub URL containing a username.

### No speculation about people

- Avoid claims about contributors, maintainers, or organizations you do not know firsthand. Stick to what is in the code in front of you.
- Do not link to private resources, paid services with tracking parameters, or invite-only communities.'))
SELECT 'discretion',
       'Conservative behaviour policy for public actions: no stranger @-mentions, no cross-repo writes, no speculation about people, no linking to private or tracking resources.',
       t, t, 1, 5
FROM text;
