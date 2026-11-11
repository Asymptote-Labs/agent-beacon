#!/bin/sh
# Deletes old versions of the smoke image packages on GHCR, keeping what CI and a rollback need.
#
#   GH_TOKEN=... sh packaging/linux/smoke/prune.sh OWNER NAME...
#
# The packages hold versions of more than one recipe: each Dockerfile change publishes under a new
# digest tag, and a workflow dispatched from a branch publishes that branch's recipe. So versions are
# judged by the recipe they belong to, never by age alone, which could let a few branch publishes
# push out the image main pulls:
#
#   - the recipe in this checkout (the one CI on this ref pulls): its newest KEEP versions stay -- the
#     current image and the dated ones a rollback points at -- and the older ones go;
#   - any other recipe, and an untagged version: kept for GRACE_DAYS, so a pull request that changed
#     a Dockerfile, or a branch that has not caught up, still pulls its image; deleted after that.
#
# Run it from main only: on a branch, "the recipe in this checkout" is the branch's, and main's
# would count as another recipe. A version GHCR refuses to delete (it does for a public version with
# more than 5,000 downloads) is reported and skipped; storage of a public package costs nothing.
set -eu

owner="${1:?usage: prune.sh OWNER NAME...}"
shift
KEEP="${KEEP:-4}"
GRACE_DAYS="${GRACE_DAYS:-30}"
dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
cutoff="$(date -u -d "$GRACE_DAYS days ago" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null \
  || date -u -v-"$GRACE_DAYS"d +%Y-%m-%dT%H:%M:%SZ)"
tab="$(printf '\t')"

for name in "$@"; do
  pkg="beacon-smoke-$name"
  current="$(sh "$dir/image.sh" --digest "$name")"
  if ! versions="$(gh api --paginate "orgs/$owner/packages/container/$pkg/versions" \
      --jq '.[] | "\(.created_at)\t\(.id)\t\(.metadata.container.tags | join(" "))"')"; then
    echo "::warning::$pkg: cannot list versions; not pruned"
    continue
  fi
  kept=0
  # Newest first, so the first KEEP versions of the current recipe are the ones kept.
  printf '%s\n' "$versions" | sort -r | while IFS="$tab" read -r created id tags; do
    [ -n "$id" ] || continue
    case " $tags " in
      *" $current "* | *" $current-"*)
        kept=$((kept + 1))
        [ "$kept" -gt "$KEEP" ] || continue
        why="older than the newest $KEEP of recipe $current" ;;
      *)
        [ "$created" \< "$cutoff" ] || continue
        why="another recipe or untagged, older than $GRACE_DAYS days" ;;
    esac
    if gh api -X DELETE "orgs/$owner/packages/container/$pkg/versions/$id" >/dev/null; then
      echo "$pkg: deleted version $id ($created; ${tags:-untagged}): $why"
    else
      echo "::warning::$pkg: could not delete version $id ($created; ${tags:-untagged}); left in place"
    fi
  done
done
