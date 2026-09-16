# NimoOS agent instructions

## Worktree cleanup after commits

- After completing and committing a task, inspect the Git worktrees for this repository.
- Once the commit is pushed or otherwise explicitly preserved on a remote/ref, remove only temporary worktrees created for that task.
- A temporary worktree may be removed only when it is clean (`git status --porcelain` is empty), no longer used by another active task, and is not the primary or current checkout.
- Never remove a worktree with uncommitted changes. Report it and leave it for the user to resolve.
- Never remove the primary checkout or use broad recursive deletion. Clean temporary worktrees must be removed with `git worktree remove <validated-worktree-path>`.
- Use `git worktree remove --force <validated-worktree-path>` only when the user explicitly authorizes discarding a dirty or otherwise unrecoverable worktree.
- Before removal, report the repository, worktree path, branch/commit, and clean/pushed checks. After removal, run `git worktree list` to verify it is gone.
- If no temporary worktree exists, state that no cleanup was needed.

## Commit and push safety

- Inspect status and the diff before committing.
- Do not force-push or overwrite remote history unless the user explicitly requests it.
- If the local branch is behind the remote, use a fast-forward update when the working tree is clean; stop and report if histories diverge.
