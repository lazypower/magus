# Magus Reconciler

**Status:** Draft
**Authors:** Chuck, Fiona (design), Claude (spec)

## Problem

Magus has two modes today: build-time (Containerfile bakes the OS) and first-boot (Ignition runs `magus.bu` once, destructively). After first boot, there's no declarative path for day-2 changes. You either rebuild the image, SSH in and hand-edit, or pray.

The gap is continuous reconciliation — taking a desired-state declaration and converging the running system toward it, repeatedly, without destroying anything.

## Stance

Two consumers read the same Butane file. They are not symmetric.

|                    | Ignition                | Magus                       |
|--------------------|-------------------------|-----------------------------|
| When               | Once, at first boot     | Continuously, day 2+        |
| Mode               | Destructive             | Additive                    |
| Authority          | Total over the disk     | Scoped to its namespace     |
| Sections consumed  | Everything              | The IR subset (see below)   |

**Magus is authoritative within its namespace and inert outside it.** Inside, declared resources are reconciled to the declared state — no apologies, no warnings. Outside, Magus is blind: it does not see, does not touch, does not warn.

**Magus is authoritative over presence and absence — for resources it owns.** If Magus owns a resource (per the manifest) and the resource is no longer declared in the IR, Magus removes it. This is bounded strictly by ownership: Magus only deletes paths in its manifest. Resources outside the manifest are invisible — Magus cannot delete what it doesn't own, even if those resources fall inside `file_roots`.

Forward-only convergence isn't reconciliation; it's accumulation. Two complementary boundaries make this safe: **policy** (pre-flight — what Magus may attempt at all) and **manifest** (post-hoc — what Magus has done, and therefore owns). They play the role that namespaces, RBAC, and ownership semantics play in Kubernetes, but enforced by Magus itself because the host filesystem doesn't give us those primitives for free. Policy lives in `/etc/magus/policy.yaml`; see the Policy section.

**Magus acquires ownership two ways:**

1. **Creation.** Magus writes the resource and records it in the manifest at apply time.
2. **Adoption.** Magus encounters a declared resource whose on-disk state already matches the IR exactly, and silently records it in the manifest. No filesystem write — only the manifest is updated.

Adoption is what makes the Ignition → Magus handoff work. At first boot, Ignition places the system. On the first `magus apply`, every declared resource Ignition wrote is adopted, and Magus takes over from there. Adoption is bounded by content match: if a declared path exists but its content differs from the IR, that's a conflict — not a silent takeover. Forced takeover is available as an explicit operation (`magus adopt`).

Adoption is a no-op for *content* — never a write. Persistent state may still be reconciled on the same apply: a unit adopted as `enabled: true` while currently disabled gets enabled. The principle is that ownership transfer never changes the bytes, but from that moment on the resource is under full reconciliation.

**Magus reconciles persistent state, not runtime state.** File content, directory existence, and unit enablement are persistent — Magus reconciles them on every apply. Whether a unit is currently active or inactive is runtime state, and that's systemd's domain. Magus does not start, stop, or watchdog services beyond the moment of creation. The unit of correctness is "the system will reach the declared state on next boot."

These are not v1 limitations. They are the contract.

```
magus.bu
  ↓  parse + validate against policy
IR  (units, drop-ins, files, directories)
  ↓  diff vs current state + manifest
plan  (create / update / skip / conflict)
  ↓  apply
systemd + filesystem
```

Butane is the LLM-facing contract. systemd is the executor. Magus is the compiler in between.

## Policy

Policy lives in a sibling file Magus ships with — **`/etc/magus/policy.yaml`** — not in the Butane file. The Butane file is the system declaration; the policy is reconciler config. They have different consumers, different change cadences, and different audit lifecycles, and embedding policy in Butane would force every Butane consumer (Ignition included) to deal with a key it doesn't own.

Policy is loaded first, on every `magus apply`, and gates everything that follows. It is the **pre-flight authority** boundary. The manifest is post-hoc (records what Magus has done); the policy is pre-flight (gates what Magus may attempt).

```yaml
# /etc/magus/policy.yaml
version: 1

file_roots:
  - /etc/magus.d
  - /etc/systemd/system
  - /var/lib/magus
  - /var/data

unit_patterns:
  - "magus-*"
  - "*.d/10-magus.conf"

deny:
  paths:
    - /etc/shadow
    - /etc/passwd
    - /etc/sudoers
    - /etc/sudoers.d/*
  units:
    - "sshd.*"
    - "systemd-*"
```

**Hard rules:**

1. **Path allowlist.** No write outside `file_roots`. Paths checked after symlink resolution.
2. **Unit namespace.** Only manage units matching `unit_patterns`.
3. **Deny list.** A path or unit matching `deny` is off-limits even if it falls inside `file_roots` / `unit_patterns`. Deny is the explicit "never touch this" override.
4. **Drop-in precedence.** All drop-ins go to `10-magus.conf` so they sort predictably and are easy to identify.
5. **Manifest ownership is binding.** Magus reconciles paths it owns (per the manifest), including removing them when they leave the IR. Paths it didn't claim are skipped — even if they fall inside `file_roots`. No "polite reconciliation."
6. **No privilege escalation.** File modes cannot exceed what the policy declares. No setuid, no world-writable.

**Policy contention with the manifest.** When a new policy denies a path Magus currently owns, Magus transitions the manifest entry to an **orphaned** state: the entry is retained (audit trail), no further reconciliation occurs, the path is excluded from diff and apply, a warning is emitted on every plan and apply. The file is left in place — never deleted, never modified.

Orphan state is sticky. Removing the deny rule does **not** automatically restore reconciliation; the entry stays orphaned until the operator runs `magus reclaim <path>`. This prevents the failure mode where a deny rule is removed by accident (or by an LLM editing the policy) and Magus silently resumes managing a path it had been told to stop touching.

The cleanup case: if an orphaned path is also removed from disk out-of-band, the manifest cleanup row drops the orphan entry on the next apply. So orphans don't accumulate forever — they age out when the underlying file is gone.

Policy is a live control plane in this model. A change to `policy.yaml` is a behavioral change, not a config tweak. Treat it like code — review, version, audit.

**Policy contention with the IR.** When the IR declares a path or unit that the policy denies, that's an input-bad case: the user wrote two contradictory things. `magus apply` halts at parse-time validation, exits non-zero, applies nothing. Resolve by editing the IR or the policy.

## IR contract

The Butane file is the input. Magus consumes a strict subset as its intermediate representation. Anything outside this subset is invisible to Magus.

**Accepted IR:**

| Resource              | Actions                                              | Notes                                                          |
|-----------------------|------------------------------------------------------|----------------------------------------------------------------|
| `systemd.units`       | Create, drop-in override, delete on omission         | Drop-ins to `10-magus.conf` only. Must match `unit_patterns`. Delete = stop + disable + unlink + daemon-reload. |
| `storage.files`       | Create, update, delete on omission                   | Atomic write. Must fall within `file_roots`.                   |
| `storage.directories` | Create, reconcile mode/ownership                     | Never removed even on IR omission — directories may hold user data Magus didn't track. v1 exception; see open questions. |

**Rejected IR (consumed only by Ignition):**

- `storage.disks`
- `storage.filesystems`
- `storage.raid`
- `storage.luks`
- `passwd.users` (deferred — see open questions)
- Any operation that partitions, formats, or mounts

**Magus does not error on Ignition-only fields. It ignores them completely. This is intentional.** The same Butane file is the input to two consumers; Ignition-only sections belong to a different consumer's vocabulary, not malformed input. A file that *also* violates the policy block (write outside `file_roots`, unit not matching `unit_patterns`) is a hard error and blocks `apply`.

## Diff model

For every resource in the union of (IR, manifest):

| In IR | On disk              | Manifest entry          | Action               |
|-------|----------------------|-------------------------|----------------------|
| yes   | absent               | —                       | **Create**           |
| yes   | present, hash match  | Magus active            | **Skip**             |
| yes   | present, hash match  | none                    | **Adopt**            |
| yes   | present, hash differ | Magus active            | **Update**           |
| yes   | present, hash differ | none                    | **Conflict**         |
| no    | present              | Magus active            | **Delete**           |
| no    | present              | none                    | Ignored              |
| no    | absent               | Magus active (stale)    | Manifest cleanup     |
| any   | any                  | Magus orphaned          | **Skip + warn**      |

"Hash match / differ" is content hash for files, normalized unit content for systemd units.

**Adopt is silent and content-bounded.** When the on-disk state already matches the IR exactly, claiming ownership changes nothing observable — the file is what Magus would have written; only the manifest is updated. This is the row that makes Ignition → Magus handoff work, and the row that supports general migration into Magus management. Adoption is reflected as `[adopt]` in plan output so it's visible, never inferred from behavior.

**Delete is bounded by ownership.** Magus only removes resources whose path is in the manifest — same boundary that governs create and update. A path inside `file_roots` that Magus didn't place is invisible to deletion.

**Directories are the one asymmetry in v1.** Files and units are fully authoritative on absence; directories are not. They are never removed, even when removed from the IR. See Apply mechanics for the reason — directories may contain user data Magus didn't track, and `rm -rf` semantics don't compose cleanly with binary ownership.

**Conflicts are skipped, not halted.** A declared path that already exists, differs from the IR, and isn't in the manifest is reported as a conflict and *skipped* — Magus does not overwrite, and it does not stop. Reconciliation continues for every other resource. The conflict is logged and surfaced in `magus status` so the operator can resolve it out-of-band.

This is the reconciler-pattern compromise. Magus runs unattended (timer-driven `magus apply --yes`); it can't pause and ask. Halting on any single conflict would mean one bad resource indefinitely blocks all convergence — the system stops working until a human notices. Per-resource skip degrades gracefully: resources that can converge do; conflicts wait for human attention without taking everything else hostage. Same posture Flux and ArgoCD take.

**What does halt the whole apply** is *input-bad* cases — bugs in the input that make safe progress impossible:

- Butane file fails to parse
- Policy file fails to parse
- IR declares a path or unit that the policy denies (parse-time validation)
- Manifest version doesn't match the binary's expectations

These are not state contention; they're broken inputs. Halting forces a human to fix them before any apply runs.

The user resolves an apply-time conflict manually (delete the path, move it out of `file_roots`, fix the IR to match, or `magus adopt --force` to take it over with the IR content). Until then the path stays in the conflict list, and every apply re-checks and re-skips.

**Stale manifest entries** — paths Magus claims to own but which are absent on disk (typically deleted out of band, or after a v1 unit-removal that didn't update the manifest cleanly) — are pruned silently during apply, with a log line for visibility. No reconciliation action is taken; the disk is already in the desired state.

A path inside `file_roots` that is neither in the IR nor in the manifest is simply ignored. Not a conflict, not Magus's problem.

## Equivalence

The diff model rests on a single question: *does the on-disk state match the IR?* The answer is a fixed equivalence relation per resource type. Equivalence is part of the contract — too strict produces false conflicts on insignificant whitespace; too loose produces unsafe adoption. The rules below are version 1 and are recorded by manifest version.

**Files (`storage.files`).** Byte-exact comparison. The hash is `sha256(content)` for both sides. Mode and ownership (uid, gid) must also match the declared values exactly. Any of (content, mode, owner, group) differing → not equivalent.

Files are opaque to Magus — no normalization, no whitespace tolerance, no encoding awareness. Same byte sequence or no match. This is the right tradeoff: file content semantics vary too much across formats (config, scripts, certificates) to attempt generic normalization.

**Directories (`storage.directories`).** Existence + mode + ownership. Directory contents are not compared (Magus does not recurse). A declared directory at the right path with the right mode and ownership is equivalent.

**systemd units / drop-ins.** Canonicalized unit content + mode + ownership. The canonicalization algorithm:

1. Drop blank lines.
2. Drop comment lines (lines whose first non-whitespace character is `#` or `;`).
3. Trim trailing whitespace from each line.
4. Normalize `key=value` spacing: collapse to `key=value` with no whitespace around `=`.
5. Preserve section headers exactly — case-sensitive, including brackets.
6. Preserve key order within each section. systemd treats several directives (`ExecStartPre`, `ExecStart`, `ExecStartPost`, `Environment`, etc.) as order-sensitive; reordering would change behavior.
7. Preserve section order across the file.

Hash is `sha256` of the canonicalized byte sequence. Two unit files that canonicalize to the same bytes are equivalent. Mode and ownership are compared exactly, same as files.

The canonicalization is intentionally lossy in only two dimensions: whitespace noise and comments. Everything else — keys, values, ordering — is preserved bit-exact. This is the "tight enough to be safe, loose enough to handle Ignition's output and human-readable formatting" line.

**Why this matters for adoption.** Adoption is a no-op only if equivalence holds. If the canonicalization is wrong (too loose), Magus could adopt a unit whose actual behavior differs from the IR. If it's too tight, every Ignition-placed unit becomes a conflict because of trailing whitespace or a comment, and the bootstrap path breaks. The rules above are picked to fail safe in both directions.

**Versioning.** The manifest's top-level `version` field tracks both the manifest schema and the equivalence rules. A reconciler binary refuses to operate on a manifest with a version it doesn't understand. Future changes to canonicalization bump the version and force a manifest migration, never a silent reclassification.

## Apply mechanics

**Files (create / update):**
1. Write to `<path>.magus.tmp`
2. Set mode and ownership on the temp file
3. `rename(2)` into place (atomic on same filesystem)
4. Update manifest with new hash and timestamp

**Files (adopt — content already matches IR):**
1. Re-verify on-disk hash equals declared hash (don't trust the plan; conditions could have changed)
2. Add the entry to the manifest with the current hash and an `adopted_at` timestamp
3. No filesystem write

**Files (delete):**
1. `unlink(2)` the path
2. Remove the entry from the manifest

**systemd units / drop-ins (create / update):**
1. Write file atomically (same as files), if content differs
2. `systemctl daemon-reload` once, after all unit writes
3. **Reconcile enablement** (persistent state, checked every apply):
   - Declared enabled, `is-enabled` says no → `systemctl enable <unit>`
   - Declared disabled, `is-enabled` says yes → `systemctl disable <unit>`
4. **First-time start, only on creation:** new unit declared enabled → `systemctl start <unit>` (combined with step 3 as `enable --now` for new units)
5. **Restart on content change**, only if the unit is currently active: `systemctl restart <unit>`
6. **Inactive units whose content changed** are rewritten only. The new content takes effect on next start. Logged at apply-time so the deferred behavior is visible.
7. Update manifest

**systemd units / drop-ins (adopt — content already matches IR):**
1. Re-verify content match
2. Add to manifest
3. Reconcile enablement per the standard rules (now that Magus owns it, persistent state is in scope)
4. No restart, no daemon-reload — content didn't change

**systemd units / drop-ins (delete):**
1. `systemctl disable --now <unit>` — stops if active, removes enablement symlinks
2. `unlink(2)` the unit file
3. Remove the entry from the manifest

**`systemctl daemon-reload` runs exactly once per apply**, after all unit filesystem changes (creates, updates, adopts, deletes) complete and before enablement and activity reconciliation. Do not reload between resources — batch all writes first.

Enablement is reconciled every apply because it's persistent. Activity is not — Magus starts a unit once on creation and otherwise leaves runtime to systemd.

**Directories:**
1. `mkdir -p` with declared mode (no-op if already exists)
2. `chown` as declared (mode and ownership are reconciled on existing directories; content is not touched)

**Directories are never deleted in v1**, even on IR omission. They may contain user data Magus didn't track, and `rm -rf` semantics don't compose with the binary-ownership model. Manual cleanup. A future version may delete *empty* Magus-owned directories.

**Per-resource error handling.** Each resource is reconciled independently. If reconciliation of a resource fails — content conflict, IO error, systemd failure — the failure is logged, the resource is marked failed in `magus status`, and apply continues with the next resource. The aggregate exit code reflects the worst outcome:

- `0` — every declared resource is in its desired state, no conflicts, no skips
- `2` — one or more resources skipped (conflicts, deferred restarts, etc.); rest converged
- `1` — one or more resources errored mid-apply (write failed, systemd command failed, etc.)

Halting only happens at policy/IR validation (input-bad cases described in Diff model). Once the apply loop starts, it runs to completion.

All operations are idempotent. Running `apply` twice with no input changes produces zero side effects.

## State tracking — the manifest

`/var/lib/magus/manifest.json` is the consent contract: it records every path Magus placed, with its hash and apply time.

```json
{
  "version": 1,
  "resources": {
    "/etc/magus.d/ollama.env": {
      "state": "active",
      "hash": "sha256:abc123...",
      "applied_at": "2026-04-26T18:30:00Z",
      "origin": "create"
    },
    "/etc/systemd/system/magus-healthcheck.timer": {
      "state": "active",
      "hash": "sha256:def456...",
      "applied_at": "2026-04-26T18:30:00Z",
      "origin": "adopt"
    },
    "/etc/shadow": {
      "state": "orphaned",
      "hash": "sha256:9f1234...",
      "applied_at": "2026-04-20T10:15:00Z",
      "orphaned_at": "2026-04-26T18:30:00Z",
      "orphaned_reason": "policy deny",
      "origin": "create"
    }
  }
}
```

**`state`** is `active` (under reconciliation) or `orphaned` (excluded; audit-only). **`origin`** records how the path entered the manifest: `create` (Magus wrote it), `adopt` (Magus encountered it matching the IR), `force-adopt` (taken over via `magus adopt`). Origin is metadata for auditability; reconciliation behavior depends only on state.

The manifest answers exactly one question: *did Magus put this here?* If yes, Magus owns it and reconciles it on every apply. If no, hands off — even if the path falls inside `file_roots`.

Ownership is binary by design. There is no "Magus wrote it but a human edited it" middle ground. If the on-disk hash diverges from the manifest hash on a path Magus owns, Magus overwrites and re-records — that's the meaning of authoritative-within-namespace.

Manual manifest editing is **unsupported** in v1. Hand-editing `manifest.json` to re-assign ownership is undefined behavior — you can end up with files Magus thinks it owns but didn't write, or vice versa. A future `magus relinquish <path>` command will provide a supported way to release Magus's claim. Until then, treat the manifest as internal state.

## CLI

### `magus plan`

Parse, diff, print. No side effects.

```
$ magus plan
config/butane/magus.bu → 5 resources

  [create]  /etc/systemd/system/magus-healthcheck.timer
  [create]  /etc/systemd/system/magus-healthcheck.service
  [adopt]   /etc/magus.d/ollama.env  (matches IR, claiming ownership)
  [delete]  /etc/systemd/system/magus-farts.service  (owned, no longer declared)
  [skip]    /var/lib/magus/state.json  (unchanged)

2 creates, 0 updates, 1 adopt, 1 delete, 1 skipped
```

Exit codes (plan): `0` = no changes needed, `2` = changes pending or conflicts present, `1` = input-bad (parse error, policy/IR contradiction, manifest version mismatch).

`--explain` augments the plan with per-resource diffs for any `[update]` or `[conflict]` actions. For units, the diff is over the canonicalized form (same bytes used for hashing). For files, it's a unified text diff. If either side is non-text, the diff is replaced with the sha256 of each side — enough to verify which version is which against external sources without dumping bytes. Mode and ownership deltas are shown as single lines.

```
$ magus plan --explain
config/butane/magus.bu → 4 resources

  [orphaned] /etc/shadow  (orphaned 2026-04-26 by policy deny — `magus reclaim` to restore)

  [conflict] /etc/systemd/system/legacy-thing.service
    --- on disk (canonical)
    +++ IR (canonical)
    @@ -3,2 +3,2 @@
     [Service]
    -ExecStart=/usr/bin/old-binary
    +ExecStart=/usr/bin/new-binary

  [update]   /etc/magus.d/ollama.env
    --- on disk
    +++ IR
    @@ -1,2 +1,2 @@
    -OLLAMA_HOST=127.0.0.1:11434
    +OLLAMA_HOST=0.0.0.0:11434
     OLLAMA_KEEP_ALIVE=24h

  [update]   /var/lib/magus/cert.der
    binary content differs
      on disk: sha256:9f1234...
      IR:      sha256:abc987...

  [skip]     /etc/systemd/system/magus-healthcheck.timer
```

### `magus apply`

Run `plan`, then execute. Requires confirmation unless `--yes` is passed.

```
$ magus apply
config/butane/magus.bu → 6 resources

  [create]   /etc/systemd/system/magus-healthcheck.timer
  [create]   /etc/systemd/system/magus-healthcheck.service
  [adopt]    /etc/magus.d/ollama.env  (matches IR, claiming ownership)
  [delete]   /etc/systemd/system/magus-farts.service  (owned, no longer declared)
  [conflict] /etc/magus.d/legacy.env  (exists, content differs, not owned)
  [skip]     /var/lib/magus/state.json  (unchanged)

Apply 4 changes? (1 conflict will be skipped) [y/N] y

  ✓ disable --now magus-farts.service
  ✓ /etc/systemd/system/magus-farts.service  (removed)
  ✓ /etc/systemd/system/magus-healthcheck.timer
  ✓ /etc/systemd/system/magus-healthcheck.service
  ✓ /etc/magus.d/ollama.env  (adopted, no write)
  ✗ /etc/magus.d/legacy.env  (skipped: conflict)
  ✓ daemon-reload
  ✓ enable --now magus-healthcheck.timer

Applied 4 changes, 1 conflict (skipped), 0 errors.  exit 2
```

### `magus status`

Structured surface for humans and LLMs.

```
$ magus status --json
{
  "last_apply": "2026-04-26T18:30:00Z",
  "result": "ok-with-skips",
  "managed_resources": 5,
  "units": {
    "magus-healthcheck.timer": "active",
    "magus-healthcheck.service": "inactive (waiting)"
  },
  "files": {
    "/etc/magus.d/ollama.env": "ok",
    "/etc/systemd/system/magus-healthcheck.timer": "ok",
    "/etc/systemd/system/magus-healthcheck.service": "ok"
  },
  "conflicts": [
    {
      "path": "/etc/magus.d/legacy.env",
      "reason": "exists, content differs from IR, not in manifest",
      "first_seen": "2026-04-26T18:30:00Z"
    }
  ],
  "orphaned": [
    {
      "path": "/etc/shadow",
      "reason": "policy deny",
      "orphaned_at": "2026-04-26T18:30:00Z"
    }
  ],
  "errors": []
}
```

`result` is one of `ok` (everything converged), `ok-with-skips` (conflicts present, or orphans warned), or `error` (mid-apply failures). `conflicts` lists every IR-declared path Magus refuses to overwrite. `orphaned` lists every path Magus once managed but no longer reconciles due to policy — kept persistently until the file is removed from disk or the operator runs `magus reclaim`.

### `magus reclaim`

Restore an orphaned path to active reconciliation. Run after the policy that caused the orphan has been removed (or amended to permit the path again). The IR must declare the path; the path must exist on disk.

```
$ magus reclaim /etc/shadow
This path is orphaned (orphaned 2026-04-26 by policy deny).

  - manifest hash:  sha256:9f1234...
  - on-disk hash:   sha256:9f1234...  (matches)
  - IR hash:        sha256:9f1234...  (matches)

Reclaiming will resume reconciliation from this state.

Reclaim /etc/shadow? [y/N] y

  ✓ /etc/shadow  (state: orphaned → active)
```

If the on-disk content has drifted during orphan, reclaim refuses unless `--force` is passed (which writes the IR content over the existing file). Reclaim never auto-runs — the operator decides when to take a path back under management.

### `magus adopt`

Take over a path that exists, differs from the IR, and isn't in the manifest. **This overwrites the existing content with the IR's content**, then records the entry in the manifest. Use it when you want Magus to own a path you're willing to replace — `terraform import` with a write step. (Adoption of *matching* content needs no command — `magus apply` does it automatically.)

```
$ magus adopt /etc/systemd/system/legacy-thing.service
The path exists with content that differs from the IR.

  - existing hash: sha256:9f12...
  - declared hash: sha256:abc1...

Overwriting will replace the existing content with the IR's declared content.

Take over /etc/systemd/system/legacy-thing.service? [y/N] y

  ✓ /etc/systemd/system/legacy-thing.service  (rewrote, recorded in manifest)
```

Adoption of *matching* content happens automatically during `magus apply` and does not require this command. `magus adopt` exists for the deliberate-overwrite case.

### `magus validate`

Parse and check against the policy block. Pure — runs anywhere, no system access.

```
$ magus validate config/butane/magus.bu
ok: 3 resources, 0 policy violations

$ magus validate bad.bu
error: /etc/shadow is denied by policy
error: sshd.service does not match unit_patterns
error: /tmp/foo is outside allowed file_roots
```

By default `magus validate` reads `/etc/magus/policy.yaml`. Override with `--policy <path>` for testing.

## Relationship to existing artifacts

| Artifact         | First boot                       | Day 2                          |
|------------------|----------------------------------|--------------------------------|
| `Containerfile`  | Bakes the OS image               | `bootc upgrade`                |
| `magus.bu`       | Ignition consumes everything     | Magus consumes the IR subset   |
| `magus.json`     | Ignition input                   | Unused                         |
| `magus` binary   | Ships in the image               | Runs on the host               |

Same source of truth. Two consumers with explicitly different authority.

## Implementation

Go. Static binary in the OS image. Dependencies: Butane library for parsing, `dbus` for systemd, `os` for filesystem. No container runtime dependency.

**v1 reads Butane and policy directly on every apply.** Policy is loaded first from `/etc/magus/policy.yaml`, the Butane file is parsed into the in-memory IR, the IR is validated against the policy, then reconciliation proceeds. A precompiled-IR-on-disk step is a v2 concern — the contract is what matters now, not the artifact.

## Open questions

1. **Trigger model.** v1 is `magus apply` (manual or shell-driven). Production deployment is a systemd timer running `magus apply --yes` on an interval. The timer is more predictable than a file watcher and matches the reconciler-pattern assumption that drove the per-resource skip rule.

3. **Rollback.** Not in v1. The manifest records what was placed but not what was *deleted* — a future rollback would need a short-retention backup dir for removed content, written before the unlink.

4. **`passwd.users`.** Deferred. If added later: append-only (create users, add to groups). No mutation, no removal. Same authority model as the rest of the IR.

5. **Testing surface.** Compiler and planner stages are pure functions (Butane in, plan out) — straightforward to unit test. Apply needs an integration shim for systemd and filesystem; TBD whether that's a real VM, a chroot, or a fake.

6. **IR vocabulary.** The IR currently borrows Butane's section names (`systemd.units`, `storage.files`, `storage.directories`). That's fine for v1 — Butane is the input format and the names are familiar. Future versions may introduce a Magus-native vocabulary that decouples the IR from the input format, especially if a precompiled IR-on-disk artifact lands.

7. **`magus relinquish <path>`.** A supported way to release Magus's claim on a path *without* deleting it — distinct from removing it from the IR (which now triggers deletion). Useful when you want to keep something Magus created but stop reconciling it, or when transitioning a path from Magus management back to manual.

8. **Empty-directory deletion.** v1 never removes directories. v2 may delete Magus-owned directories that are empty at apply time, behind a per-directory opt-in flag in the IR. Skipped for now because empty-at-check-time isn't empty-at-rmdir-time, and the safety analysis isn't worth doing yet.

