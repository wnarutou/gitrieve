# Git history preservation

Gitrieve retains downloaded history when upstream branches disappear, change
names, or are force-pushed. Use `useCache: true` to retain the local repository
between jobs, and `depth: 0` when full history is required. A shallow archive can
only preserve the history it downloaded.

## Missing upstream branches

An upstream-deleted branch is an informational event, not a synchronization
failure. Gitrieve logs the missing branch, retains its local branch, tracking
reference and archived history, and continues synchronizing surviving branches.
The tracking-name conflict handling described below still applies.

If a branch disappears between reference discovery and fetch, Gitrieve rechecks
the upstream list and retries only the surviving branches from the original
plan. Each retry must remove at least one missing branch, so repeated deletions
cannot cause an unbounded retry loop. Newly discovered branches wait for the next
sync's preservation and path-conflict checks. If a new default branch was already
in the original plan, it becomes the checkout; otherwise the current checkout is
kept until the next sync. An upstream with no remaining branches keeps the cached
checkout and history. If the server omits HEAD after deleting the cached default
branch, the cached checkout is retained while surviving branches synchronize.
An otherwise unexplained invalid remote HEAD still stops synchronization.

Reference discovery, authentication, network and storage failures still stop
synchronization. A missing-ref error without a confirmed missing upstream branch
also remains an error.

## Branch path conflicts

A cached reference such as `origin/noa/v8` can prevent fetching a newer upstream
branch named `noa/v8/new`. The reverse transition can also conflict.

During synchronization, Gitrieve:

1. Reads the current upstream branch list. If this fails, references stay unchanged.
2. Pins existing origin tracking tips in permanent archive references.
3. Verifies those archive references before removing only the tracking names that
   block incoming branches. Unrelated stale tracking names remain in place.
4. Fetches the advertised branches and pins the resulting tips as well.
5. Creates or fast-forwards local branches. Existing local branches are never
   deleted or forcibly moved. A divergent upstream tip stays available through
   its tracking and archive references.

If an existing local branch has a conflicting name, the new branch receives a
stable `gitrieve-<SHA-256>` alias, with a numeric suffix if necessary. Its upstream
mapping is stored in the ordinary `[branch "..."]` section of `.git/config`.
Retries and subsequent updates reuse that mapping, including when the upstream
default branch needs an alias.

Conflict handling removes only verified tracking references and empty reference
directories. It does not clear the repository cache, delete object files, or
run a blanket prune. The existing per-repository component lock covers the operation.

## Recover an archived tip

Archive references have this format:

```text
refs/gitrieve/archive/<hex-encoded-original-reference>/tip-<commit-hash>
```

The original reference includes `refs/remotes/origin/`. Its UTF-8 bytes are encoded
as lowercase hexadecimal, split into directory components of at most 120
characters. Joining those components and decoding the hex recovers the original
name. The leaf includes the commit hash, so successive tips coexist without
overwriting one another. These references keep their reachable Git objects alive
during ordinary garbage collection.

Run this inside the cached repository to list saved commits:

```bash
git for-each-ref --format='%(objectname) %(refname)' refs/gitrieve/archive/
```

To list the original branch names too, use Python 3 inside that repository:

```python
import subprocess

prefix = "refs/gitrieve/archive/"
lines = subprocess.check_output([
    "git", "for-each-ref", "--format=%(objectname) %(refname)", prefix
], text=True).splitlines()
for line in lines:
    commit, ref = line.split(" ", 1)
    encoded = "".join(ref[len(prefix):].split("/")[:-1])
    print(commit, bytes.fromhex(encoded).decode("utf-8"))
```

Create a recovery branch from the desired hash:

```bash
git branch recovered-noa-v8 <commit-hash>
```

On Windows, enable `git config core.longpaths true` before using native Git on
deep cache paths, including before running garbage collection. Gitrieve's Go
filesystem operations support paths that native Git otherwise may fail to traverse.

The `.tar.gz` archive includes `.git` and these references. Restore that full
directory to keep the saved history; a normal Git clone does not automatically
copy this custom reference namespace.

## Cancellation and retry

Archive writes are verified before conflicting tracking names are released. If
verification fails, synchronization stops with the original reference still in
place. An interruption after relocation leaves the saved tip recoverable, and
the next run can finish fetching the new branch.

The `.git/gitrieve-sync-pending` marker records changes awaiting a successful
archive upload. It survives cancellation and storage failures, so a subsequent
job uploads the archive even when Git fetch reports no new changes. It is cleared
only after the sync and all configured storage writes succeed.

Archive references accumulate as branch tips change; Gitrieve does not expire
them automatically. Existing archive filename/versioning and `useCache: false`
cleanup behavior are unchanged.
