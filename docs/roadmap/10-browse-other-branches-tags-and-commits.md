# 10. Browse other branches, tags and commits

**Seen in:** Sourcegraph (`rev:`), Google Code Search (branch selector).

## Problem
The file view can show a single file as of a past commit (from History), but you cannot browse a directory tree or navigate between files at another branch, tag or commit.

## Proposal
- A revision selector in the file header listing branches and tags (from `git for-each-ref`), plus free-form commit input.
- Directory listings and file views at that revision via `git ls-tree` / `git show`, keeping the revision in the URL (`?rev=`) while navigating, with a banner and a "back to working tree" link.
- Search stays on the indexed working tree (state this in the UI); indexing other branches can be a later addition.

## Acceptance criteria
- From any file in a git repository, switching to a tag shows that tag's version, and clicking through directories and files stays on the tag.
- Works with nested repositories (each folder resolves its own git work tree).

## Status: done

- A revision selector in the file and folder header lists the branches and tags
  of the git repository containing the current file (`/api/refs`), plus a
  "Working tree" entry; any commit hash or expression such as `HEAD~3` can be
  typed in.
- Folders at a revision come from `git ls-tree`, files from `git show`; the
  breadcrumbs, folder listings and file tree carry `?rev=` so navigation stays
  on the revision, with a banner linking back to the working tree.
- Revisions are validated (no leading `-`, no `..`, `:` or spaces) and paths
  are always passed after `--`. Folders outside any git repository (a folder
  holding many repositories) are shown as they are.
- Search covers the indexed working tree only, as the banner's tooltip says.
