#!/usr/bin/bash
# core-reconcile — one GitOps tick for the workload layer.
#
# Distribution is git; lifecycle + safety is magus. We pull this host's
# declaration from the (private) core-fleet repo, then let `magus apply`
# converge the workload layer — creating/updating/removing quadlets bounded by
# its ownership manifest and policy (delete-on-omission, no clobbering
# out-of-band edits).
#
# OPSEC: this script and the substrate contain NO infrastructure specifics.
# The fleet repo URL + read-only credential come from /etc/core/reconcile.env,
# injected at install via Ignition — never baked into the image.
set -euo pipefail

envfile=/etc/core/reconcile.env
# shellcheck source=/dev/null
[ -f "$envfile" ] && . "$envfile"

: "${CORE_FLEET_REPO:?CORE_FLEET_REPO unset — drop it in $envfile via Ignition}"

checkout=/var/lib/core/fleet
host=$(hostname -s)
# Track a fixed branch, never the remote's default HEAD. A freshly-created or
# re-pushed Gitea repo may have no default branch set, so an unpinned clone
# follows a dangling HEAD and lands empty — pin the branch on BOTH paths.
branch="${CORE_FLEET_BRANCH:-main}"
# Per-host layout in core-fleet: hosts/<hostname>/workload.bu is the magus
# source (install.bu in the same dir is install-time only; magus never reads it).
hostbu="${checkout}/hosts/${host}/workload.bu"

mkdir -p "$(dirname "$checkout")"
if [ -d "${checkout}/.git" ]; then
  git -C "$checkout" fetch --quiet --depth 1 origin "$branch"
  git -C "$checkout" reset --hard --quiet FETCH_HEAD
else
  git clone --quiet --depth 1 --branch "$branch" "$CORE_FLEET_REPO" "$checkout"
fi

if [ ! -f "$hostbu" ]; then
  echo "core-reconcile: no declaration for ${host} (hosts/${host}/workload.bu) — nothing to converge" >&2
  exit 0
fi

# Effective policy authority lives in core-fleet now (co-evolves with the
# workload it bounds). Precedence: per-host override -> fleet default -> the
# substrate's baked fail-closed fallback. magus loads exactly one file (no
# merge), so a per-host policy.yaml must be a complete policy. A host is NEVER
# reconciled without a policy.
policy="${checkout}/hosts/${host}/policy.yaml"
[ -f "$policy" ] || policy="${checkout}/policy.yaml"
[ -f "$policy" ] || policy=/etc/magus/policy.yaml

# magus reconciles only what this policy + its manifest allow; the substrate is
# out of scope. --yes because this runs unattended on a timer.
exec magus apply --yes --policy "$policy" "$hostbu"
