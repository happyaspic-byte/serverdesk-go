#!/bin/sh
# CI-only signed tag classification and exact candidate seal. This helper never
# publishes stable releases or treats user-supplied JSON as approval.
set -eu
fail() { echo "[FAIL] $*" >&2; exit 1; }
for command in jq sha256sum find grep sort; do
  command -v "$command" >/dev/null 2>&1 || fail "required release command is unavailable: $command"
done
case "${GITHUB_REF_NAME:-}" in
  '') fail 'missing tag' ;;
esac
printf '%s\n' "$GITHUB_REF_NAME" | grep -E -q '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$' || fail 'invalid release tag'
kind=stable
case "$GITHUB_REF_NAME" in *-*) kind=prerelease ;; esac
case "${1:-}" in
  classify)
    command -v gh >/dev/null 2>&1 || fail 'gh is required'
    ref=$(gh api "repos/${GITHUB_REPOSITORY:?}/git/ref/tags/$GITHUB_REF_NAME")
    tag_object=$(printf '%s' "$ref" | jq -er 'select(.object.type == "tag") | .object.sha') || fail 'signed annotated tag required'
    printf '%s\n' "$tag_object" | grep -E -q '^[0-9a-f]{40}$' || fail 'invalid tag object'
    tag=$(gh api "repos/$GITHUB_REPOSITORY/git/tags/$tag_object")
    printf '%s' "$tag" | jq -e --arg tag "$GITHUB_REF_NAME" --arg commit "${GITHUB_SHA:?}" \
      '.verification.verified == true and .tag == $tag and .object.type == "commit" and .object.sha == $commit' >/dev/null ||
      fail 'tag verification or commit identity mismatch'
    printf 'kind=%s\ntag_object=%s\n' "$kind" "$tag_object"
    ;;
  seal)
    [ "$#" -eq 2 ] || fail 'seal requires directory'
    dist=$2
    [ -d "$dist" ] && [ ! -L "$dist" ] || fail 'unsafe candidate directory'
    [ "$(find "$dist" -mindepth 1 -maxdepth 1 | wc -l)" -eq 4 ] || fail 'candidate must contain exactly two archives and two SBOMs'
    files='{}'
    for name in "serverdesk-$GITHUB_REF_NAME-linux-amd64.tar.gz" "serverdesk-$GITHUB_REF_NAME-windows-amd64.zip" \
      serverdesk-linux-amd64.cdx.json serverdesk-windows-amd64.cdx.json; do
      [ -f "$dist/$name" ] && [ ! -L "$dist/$name" ] || fail "missing or unsafe candidate file: $name"
      digest=$(sha256sum "$dist/$name")
      digest=${digest%% *}
      files=$(printf '%s' "$files" | jq -c --arg name "$name" --arg digest "$digest" '. + {($name): $digest}')
    done
    printf '%s\n' "${SERVERDESK_TAG_OBJECT:-}" | grep -E -q '^[0-9a-f]{40}$' || fail 'missing verified tag identity'
    printf '%s\n' "${GITHUB_SHA:-}" | grep -E -q '^[0-9a-f]{40}$' || fail 'missing commit identity'
    jq -n --arg repository "${GITHUB_REPOSITORY:?}" --arg commit "$GITHUB_SHA" \
      --arg tag "$GITHUB_REF_NAME" --arg tag_object "$SERVERDESK_TAG_OBJECT" \
      --arg workflow "${GITHUB_WORKFLOW_REF:?}" --arg run_id "${GITHUB_RUN_ID:?}" \
      --arg run_attempt "${GITHUB_RUN_ATTEMPT:?}" --arg kind "$kind" --argjson files "$files" \
      '{schema:1,repository:$repository,commit:$commit,tag:$tag,tag_object:$tag_object,
        workflow:$workflow,run_id:$run_id,run_attempt:$run_attempt,kind:$kind,files:$files,
        stable_promotion:"blocked: separately authorized protected approval and archive-bound UAT required"}' \
      > "$dist/candidate-manifest.json"
    (cd "$dist"; sha256sum serverdesk-* candidate-manifest.json | sort -k2 > SHA256SUMS)
    echo '[OK] sealed candidate archives, SBOMs, manifest, checksums; stable promotion BLOCKED'
    ;;
  *) fail 'usage: serverdesk-release.sh classify|seal DIRECTORY' ;;
esac
