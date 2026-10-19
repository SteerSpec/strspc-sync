---
name: commit
description: Write a conventional commit for the staged changes, following org standards
---

# Commit

Create a commit for the currently staged changes.

1. Run `git diff --cached --stat` and read the diff. Do not stage anything yourself.
2. Pick the type from the change, not the ticket: `feat`, `fix`, `docs`, `refactor`, `test`,
   `chore`, `ci`.
3. Write the subject as `<type>(<scope>): <imperative summary>`, lower case, no trailing period,
   72 characters or fewer.
4. In the body, explain why the change was needed and what alternatives were rejected. Wrap at 72
   characters. Reference the issue as `Refs #<n>` or `Closes #<n>` on its own line.
5. Show the message and wait for confirmation before running `git commit`.

Never amend, rebase or force-push as part of this skill.
