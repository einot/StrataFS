# Exemptions from the state and policy rules

This is the register that
[ADR 0018](0018-state-machines-and-policies-are-a-standing-rule.md) §4 keeps:
every exemption from its rules that an ADR has granted, in force or revoked.
It is not an ADR, and editing it revises none. A row restates the ADR it
cites, which is the authority for the row's code and reason; where the two
disagree, the row is the bug.

The rules are ADR 0018 §1's: the state rule, the policy rule and the link.

**How it changes** (ADR 0018 §4):

- The PR that carries an ADR granting an exemption adds its row, under the
  next number.
- The PR that carries an ADR revoking one marks its row "revoked by
  ADR NNNN".
- The PR whose ADR restates one, with a reason that still holds, adds that
  ADR to its row.
- No row is removed or renumbered, and no number is reused.

| | What | Code | Exempt from | Granted by | Status |
|---|---|---|---|---|---|
| E1 | The write budget | `budget`, in `internal/blobfs/budget.go` | the state rule | ADR 0018 §4, with ADR 0014 §1's reason | in force |
| E2 | The duplicate request cache | `replyCache` and `cacheState`, in `internal/sunrpc/cache.go` | the state rule | ADR 0018 §4, with ADR 0014 §1's reason | in force |
| E3 | `Sync`'s phases, the commit's flight and the locks that order them | `(*FS).Sync`, `(*FS).seal`, `(*FS).commitSealed` and `(*FS).applyLocked`, in `internal/blobfs/commit.go`; `FS.commitGate` and `FS.commitMu`, in `internal/blobfs/fs.go` | the state rule | ADR 0018 §4, with ADR 0014 §1's reason; restated by ADR 0023 §8, which adds the flight and `FS.commitMu` | in force |
| E4 | The per-index buffered state | `openFile.dirty` and `openFile.pendingTrim`, in `internal/blobfs/fs.go` | the state rule | ADR 0018 §4, with ADR 0014 §1's reason | in force |
| E5 | The unsettled mark | `openFile.unsettled`, in `internal/blobfs/fs.go` | the state rule | ADR 0018 §4, with ADR 0014 §1's reason | in force |
| E6 | The XDR reader's sticky error | `xdr.Reader`'s `err`, read through `Err`, in `internal/xdr/xdr.go` | the state rule | ADR 0018 §4, with ADR 0014 §1's reason | in force |
| E7 | The store's retries | `(*S3).do`, in `internal/store/s3.go` | the state rule | ADR 0018 §4, with ADR 0014 §1's reason | in force |
| E8 | The flag checks | `chunkSizeBytes`, `cacheBytes`, `commitInterval` and `ownerID`, in `cmd/strata/main.go` | the policy rule | ADR 0018 §4, which restates ADR 0014 §1's reason | in force |
| E9 | `-check` | `runCheck` and its probes, in `cmd/strata/check.go` | the state rule | ADR 0018 §4, with ADR 0014 §1's reason | in force |
| E10 | The `MaxFileSize` clamp | the clamp in `(*FS).Write`, in `internal/blobfs/fs.go` | the policy rule | ADR 0018 §4, with ADR 0014 §1's reason | in force |
| E11 | The mount gate | `(*FS).mutable`, in `internal/blobfs/fs.go` | the policy rule's "in `internal/policy`" | ADR 0018 §4 | in force |
| E12 | `ACCESS`'s read-only mask | the mask in `(*FS).Access`, in `internal/blobfs/fs.go` | the policy rule's "in `internal/policy`" | ADR 0018 §4 | in force |
| E13 | The namespace dirty flag | `FS.dirty`, in `internal/blobfs/fs.go` | the state rule | ADR 0023 §8, with ADR 0018 §1's reasons (all pairs legal, a hot path, pinned and tested elsewhere) | in force |
