# go-fossillib

`go-fossillib` is a Go reader and writer for [Fossil](https://fossil-scm.org)
repositories. It is the Fossil counterpart of the `pkg/gitlib` package in
[Gitus](https://github.com/GitusCodeForge/Gitus) and of
[go-hglib](https://github.com/GitusCodeForge/go-hglib).

The repository database is a single SQLite file whose internal tables have
changed between Fossil releases. This package therefore never opens that file
as a database. It asks the `fossil` executable for artifacts and parses the
artifact text, which the Fossil project documents as its durable interchange
format. Nothing here depends on the SQLite schema.

The package does assume a reasonably recent Fossil. It was developed against
Fossil 2.28.

## Quick start

```go
import "github.com/GitusCodeForge/go-fossillib"

repo, err := fossillib.NewLocalFossilRepository("/srv/fossil/example.fossil")
if err != nil {
    log.Fatal(err)
}
defer repo.Close()

tip, err := repo.GetTip()
fmt.Println(tip.Id, tip.CommitMessage, tip.AuthorInfo)

// History, newest first.
history, err := repo.GetCommitHistoryN(tip.Id, 50)

// A directory listing and a file at a check-in.
tree, err := repo.GetTree(tip, "src")
for _, item := range tree.ObjectList {
    fmt.Println(item.Name, item.Mode, item.IsDir)
}
content, err := repo.GetFileContent(tip, "src/main.go")

// Branches and tags. Fossil treats branch names as tags too, so
// Branch.IsBranch distinguishes them.
branches, err := repo.GetAllBranchList()
tags, err := repo.GetAllTagList()
fmt.Println(branches, tags)
```

A check-out directory is also accepted:

```go
repo, err := fossillib.NewLocalFossilRepository("/srv/checkouts/example")
branch, err := repo.GetWorkingDirBranch()
version, parent, err := repo.GetWorkingDirParents()
```

## Reading model

Fossil is artifact-oriented. There are no revision numbers and no single
typed object store:

* a **check-in** is a manifest artifact; the manifest is also the complete
  file list;
* a **file version** is a content artifact;
* a **branch** is a tag (`branch` property) carried by check-ins;
* a **tag** is a symbolic tag (`sym-...`);
* **delta manifests** (`B` cards) are resolved against their baseline
  manifest automatically.

The API exposes this directly:

| gitlib / go-hglib | go-fossillib |
| --- | --- |
| `LocalGitRepository` / `LocalHgRepository` | `LocalFossilRepository` |
| `NewLocalGitRepository` / `NewLocalHgRepository` | `NewLocalFossilRepository` |
| `CommitObject` | `CommitObject` (a check-in) |
| `TreeObject` | `TreeObject` (a directory view over the flat manifest) |
| `BlobObject` | `BlobObject` (a content artifact) |
| `Branch` | `Branch` (may have several leaves) |
| `Tag` | `Tag` (branch names appear too; `IsBranch` marks them) |
| `GetCommitHistoryN` | `GetCommitHistoryN` |
| `GetAllChangesets` | `GetAllChangesets` (every check-in, all branches) |
| `ReadObject` | `GetCommit`, `GetManifest`, `GetFile`, `ReadBlob` |
| `ResolveTreePath` | `ResolveTreePath` |
| `GetDiff` | `GetDiff` / `GetDiffBetween` |
| `Blame` | `Blame` / `BlameAt` |
| `ResolveLastCommitAtDir` | `ResolveLastCommitAtDir` |
| `AddFileToRepoString` | `AddFileToRepoString` |
| `AddMultipleFileToRepoString` | `AddMultipleFileToRepoString` |
| `Merge` | `Merge` |
| `CheckBranchMergeConflict` | `CheckBranchMergeConflict` |
| hooks directory | `GetHooks`, `AddHook`, `EditHook`, `DeleteHook` |

`CommitObject.Rev`, `P1` and `P2` are always `-1` because Fossil has no
revision numbers. Use `Id` and `ParentIdList`.

## Performance

A Fossil process costs several milliseconds, so the package minimizes the
number of processes per page and caches immutable artifacts:

* **Commit log pages** use one `fossil json timeline` call for the whole page.
  `GetBranchCommitHistoryN` accepts a `Branch` and returns the page in one
  process. `GetCommitHistoryN(cid, n)` first tries the same fast path when
  `cid` is the branch head, then falls back to walking first parents.
* **Whole-tree pages** reuse a parsed manifest. `GetTree` does not spawn one
  process per directory. Delta manifests are resolved once and cached.
* **Changed-file lists** are computed by comparing the check-in manifest with
  its primary parent's manifest, reusing cached artifacts instead of running
  `fossil diff`.
* **Last-commit-per-directory** runs one `fossil ls -H` for the whole tree and
  resolves all abbreviated hashes with one `fossil json timeline` page. A
  single short `fossil json artifact` call per entry is only a fallback for
  very old files that fall outside that page.
* **Tag lists** resolve tags in parallel. Branch lists resolve leaves from one
  `fossil leaves` call.
* **Blame** resolves every abbreviated hash in the annotation with one
  timeline page instead of one process per line.

Parsed commits and manifests are immutable and stay cached. Branch heads, the
tag set, project metadata and name-to-hash resolutions are invalidated when
the repository file changes. Call `Refresh` after another process pushes if
the same handle remains open for a long time.

Rough numbers from a 50-commit repository on an i7-1185G7 (Fossil 2.28, warm
page cache, one process has a floor of about 6-9 ms on this machine):

| Operation | Processes | Typical time |
| --- | --- | --- |
| `GetBranchCommitHistoryN` | 1 | ~7 ms |
| `GetCommitHistoryN` from the branch head | 2 | ~15 ms |
| `GetTreeAt` | 1-2 | ~10-13 ms |
| `GetFileContent` (manifest cached) | 1 | ~6 ms |
| `ResolveLastCommitAtDir` | 2 + fallbacks | ~20-30 ms |
| `Blame` | partial check-out + annotate | tens of ms |

Use one handle per repository and keep it for the lifetime of a request
worker; constructing a handle is cheap, but the caches live on the handle.

## Writing from a web interface

Fossil cannot commit without an open check-out. The writing helpers open a
temporary **`--keep` check-out**: this sets the check-out version and branch
without materializing the whole tree. Only the files being changed are
written, and the commit names exactly those paths. The repository's own
check-out, if any, is never touched.

```go
hash, err := repo.AddFileToRepoString(
    "trunk", "README.md",
    "Jane Doe", "jane@example.com",
    "Jane Doe", "jane@example.com",
    "add README",
    "# Hello\n",
)
```

A branch that does not exist yet is created at commit time, matching Fossil's
usual idiom. Several files can be written at once with
`AddMultipleFileToRepoString`; the returned value is the new check-in hash.

These helpers extend the branch's newest leaf. A branch with several open
leaves is rare in Fossil but possible; extending one of them can be rejected
by Fossil as a fork. Callers that allow multi-head branches should merge or
reopen explicitly before writing.

Fossil separates the **login** used for permission checks from the **user
name** recorded in the check-in. `--user-override` supplies the latter. If
Fossil cannot determine the current login (common in a server process), the
library picks a repository login with check-in capability automatically. Pin
it explicitly when the repository has a restricted permission matrix:

```go
repo.SetCommitUser("developer")
```

Merging and blame need a working tree:

```go
check, err := repo.CheckBranchMergeConflict("feature", "trunk")
if check.Successful {
    err = repo.Merge("trunk", "feature", "Jane Doe", "jane@example.com")
}

blame, err := repo.BlameAt(tip.Id, "src/main.go")
```

`Merge` uses a full temporary check-out because Fossil's merge engine needs
one. `Blame` uses a `--keep` check-out and materializes only the one file.

## Branches, tags, hooks, remotes and settings

```go
repo.CreateBranch("feature", baseHash) // normally done at commit time instead
repo.CloseBranch("feature")
repo.ReopenBranch("feature")

repo.AddTag("v1.0", hash, "")
repo.CancelTag("v1.0", hash)

hooks, _ := repo.GetHooks()
repo.AddHook("make test", "before-commit", 1)

repo.SetRemote("origin", "https://example.com/project.fossil")
repo.FetchRemote("origin")
repo.PushRemote("origin")

value, _ := repo.GetSetting("autosync")
repo.SetSetting("autosync", "0")
```

`GetAllSettings`, `UnsetSetting`, `GetConfigArea`, `FindTag`, `GetTagsAt`,
`SyncAllBranchList`, `SyncAllTagList` and `ClearLastCommitCache` are also
available. `CloneFossilRepository(uri, path)` clones a repository file
without opening a check-out.

## Not implemented

* The Fossil wire protocol. This package operates on local repository files.
* Direct SQLite access. That is deliberate; use `fossil sql` yourself if you
  need a query the CLI does not expose.
* Ticket, wiki, forum and technote rendering. Their artifacts are classified
  by `ParseArtifact`, but this package focuses on source-code hosting.
* Private-branch and unversioned-content synchronization options beyond what
  the remote helpers expose.
* Automatic multi-head branch repair. Fossil can have several leaves per
  branch; the writers extend the one Fossil resolves for the branch name.

## Tests

The tests create real repositories with the `fossil` executable and skip when
it is not installed.

```sh
go test ./...
```

If your `/tmp` is mounted `noexec` (common on hardened systems), point Go's
temporary directory at a directory that allows execution:

```sh
mkdir -p .gotmp
GOTMPDIR=$PWD/.gotmp go test ./...
GOTMPDIR=$PWD/.gotmp go test -run '^$' -bench . ./...
```
