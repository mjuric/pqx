## Feature work with subagents

For a feature (or set of features) big enough to split up:
1. Plan it and get my approval before starting.
2. Open an integration branch with a draft PR into `master`; commit the design and plan there (`docs/design/<feature>.md`).
3. Subagents each work on their own branch started from the integration branch (in separate worktrees), commit and push after each logical unit, keep `pytest` and `ruff` green, and open a PR into the integration branch when done.
4. You integrate: review each PR, have it independently reviewed by a separate agent if it's large, send fixes back, merge into the integration branch, and run the full test suite after each merge.
5. When everything is in and CI is green, ask me before merging the integration branch into `master`.
