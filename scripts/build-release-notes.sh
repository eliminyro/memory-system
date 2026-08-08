#!/usr/bin/env bash
#
# build-release-notes.sh — harvest human-readable release notes for a commit
# range from the descriptions of the pull requests merged in it.
#
# For PREV_TAG..REF it extracts each squash-merge PR number from the commit
# subjects ("... (#NN)"), reads each PR via `gh`, picks an entry text
# (a designated "## Release notes" section of the body, else the first
# paragraph, else the PR title), groups by the conventional-commit type parsed
# from the PR title, and renders Markdown sections. A single shared render path
# emits BOTH NOTES.md (the GitHub Release body) and the dated CHANGELOG.md
# section, so the two always agree.
#
# Deterministic; no LLM. Auth is the caller's gh token (the workflow's automatic
# GITHUB_TOKEN in CI). Harvested PR text lands only as non-executable Markdown.
#
# Usage:
#   build-release-notes.sh [PREV_TAG] [REF]
#     PREV_TAG  previous release tag (arg 1, or $PREV_TAG). Empty / missing /
#               non-existent tag => first release: range starts at the root commit.
#     REF       range end (arg 2, or $HEAD; default HEAD).
#
# Env:
#   VERSION                 if set, also write a dated CHANGELOG section.
#   NOTES_FILE              Release-body output path      (default: NOTES.md).
#   CHANGELOG_SECTION_FILE  dated changelog section path  (default: CHANGELOG_SECTION.md).
#   GITHUB_REPOSITORY       owner/repo slug (falls back to `gh repo view`).
#   GITHUB_SERVER_URL       server base    (default: https://github.com).
#
# NOTE: never `set -x` here — no token appears in this script, and tracing could
# echo one if the environment ever changes. Keep tracing off.
set -euo pipefail

PREV_TAG="${1:-${PREV_TAG:-}}"
REF="${2:-${HEAD:-HEAD}}"
NOTES_FILE="${NOTES_FILE:-NOTES.md}"
CHANGELOG_SECTION_FILE="${CHANGELOG_SECTION_FILE:-CHANGELOG_SECTION.md}"

# Repository slug + server, for absolute PR links in the rendered notes.
if [ -n "${GITHUB_REPOSITORY:-}" ]; then
  slug="$GITHUB_REPOSITORY"
else
  slug="$(gh repo view --json nameWithOwner --jq .nameWithOwner 2>/dev/null || echo "eliminyro/memory-system")"
fi
server="${GITHUB_SERVER_URL:-https://github.com}"
repo_url="${server}/${slug}"

# Resolve the range start. An empty or non-existent PREV_TAG means "first
# release" -> start at the root commit (design D3 / first-release fallback).
if [ -z "$PREV_TAG" ] || ! git rev-parse -q --verify "${PREV_TAG}^{commit}" >/dev/null 2>&1; then
  start="$(git rev-list --max-parents=0 "$REF" | tail -n1)"
else
  start="$PREV_TAG"
fi
range="${start}..${REF}"

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT
feat_file="$workdir/features"
fix_file="$workdir/fixes"
other_file="$workdir/other"
: >"$feat_file"
: >"$fix_file"
: >"$other_file"

# --- text helpers -----------------------------------------------------------

# Content under a "## Release notes" heading (levels 2-6), up to the next
# heading of any level or EOF.
extract_release_notes_section() {
  awk '
    /^#{1,6}[[:space:]]/ {
      if (grab) { exit }
      if (tolower($0) ~ /^#{2,6}[[:space:]]+release[[:space:]]+notes[[:space:]]*$/) { grab=1; next }
    }
    grab { print }
  '
}

# Remove HTML comment spans, including multi-line ones (so an unfilled template
# hint collapses to nothing and the fallback chain fires).
strip_html_comments() {
  awk '
    {
      line = $0; out = ""
      while (1) {
        if (incomment) {
          p = index(line, "-->")
          if (p == 0) { line = ""; break }
          line = substr(line, p + 3); incomment = 0
        } else {
          p = index(line, "<!--")
          if (p == 0) { out = out line; break }
          out = out substr(line, 1, p - 1); line = substr(line, p + 4); incomment = 1
        }
      }
      print out
    }
  '
}

# First maximal run of non-blank, non-heading lines.
first_paragraph() {
  awk '
    /^[[:space:]]*$/ { if (started) exit; else next }
    /^#{1,6}[[:space:]]/ { next }
    { started = 1; print }
  '
}

# Collapse to a single trimmed line and drop a leading list marker.
oneline() {
  tr '\n' ' ' | tr -s '[:space:]' ' ' \
    | awk '{ $1 = $1; print }' \
    | awk '{ sub(/^[-*][[:space:]]+/, ""); print }'
}

has_text() { [ -n "$(tr -d '[:space:]')" ]; }

# Map a title/subject to a section key: features | fixes | other | skip.
group_of() {
  local title="$1" type
  type="$(printf '%s' "$title" \
    | awk 'match($0, /^[a-zA-Z]+(\([^)]*\))?!?:/) { s = substr($0, RSTART, RLENGTH); sub(/[(!:].*/, "", s); print tolower(s) }')"
  case "$type" in
    feat) echo features ;;
    fix) echo fixes ;;
    docs | test | chore | cleanup) echo skip ;;
    *) echo other ;;
  esac
}

# Pick the entry text for a PR: release-notes section -> first paragraph -> title.
build_entry() {
  local body="$1" title="$2" section para
  section="$(printf '%s\n' "$body" | extract_release_notes_section | strip_html_comments)"
  if printf '%s' "$section" | has_text; then
    printf '%s' "$section" | oneline
    return
  fi
  para="$(printf '%s\n' "$body" | strip_html_comments | first_paragraph)"
  if printf '%s' "$para" | has_text; then
    printf '%s' "$para" | oneline
    return
  fi
  printf '%s' "$title" | oneline
}

# --- harvest ----------------------------------------------------------------

count=0
while IFS=$'\t' read -r _sha subject; do
  [ -n "$subject" ] || continue
  num="$(printf '%s' "$subject" | grep -oE '#[0-9]+' | tail -n1 | tr -d '#' || true)"

  if [ -n "$num" ]; then
    if pr_json="$(gh pr view "$num" --repo "$slug" --json title,body,labels 2>/dev/null)"; then
      title="$(printf '%s' "$pr_json" | jq -r '.title // ""')"
      body="$(printf '%s' "$pr_json" | jq -r '.body // ""')"
    else
      # PR unreachable (deleted / fork / API hiccup): fall back to the subject.
      title="$subject"
      body=""
    fi
    link=" ([#${num}](${repo_url}/pull/${num}))"
  else
    # Direct-to-master commit with no PR reference.
    title="$subject"
    body=""
    link=""
  fi

  grp="$(group_of "$title")"
  [ "$grp" = skip ] && continue

  entry="$(build_entry "$body" "$title")"
  [ -n "$entry" ] || continue
  line="- ${entry}${link}"

  case "$grp" in
    features) printf '%s\n' "$line" >>"$feat_file" ;;
    fixes) printf '%s\n' "$line" >>"$fix_file" ;;
    other) printf '%s\n' "$line" >>"$other_file" ;;
  esac
  count=$((count + 1))
done < <(git log --no-merges --pretty=format:'%H%x09%s' "$range")

# --- render (shared path for NOTES.md and the CHANGELOG section) -------------

render_body() {
  local out="$1" wrote=0 pair key ttl f
  : >"$out"
  for pair in "features:Features" "fixes:Bug fixes" "other:Other changes"; do
    key="${pair%%:*}"
    ttl="${pair#*:}"
    case "$key" in
      features) f="$feat_file" ;;
      fixes) f="$fix_file" ;;
      other) f="$other_file" ;;
    esac
    [ -s "$f" ] || continue
    [ "$wrote" -eq 1 ] && printf '\n' >>"$out"
    printf '### %s\n\n' "$ttl" >>"$out"
    cat "$f" >>"$out"
    wrote=1
  done
}

render_body "$NOTES_FILE"

# Dated CHANGELOG section from the SAME body, so file and Release always match.
# When the body is empty (only excluded commit types), the release falls back to
# GoReleaser's grouped-commit changelog (design D5); the file records that and
# points at the Release for the full commit log.
if [ -n "${VERSION:-}" ]; then
  ver_display="${VERSION#v}"
  today="$(date -u +%Y-%m-%d)"
  {
    printf '## [%s] - %s\n\n' "$ver_display" "$today"
    if [ -s "$NOTES_FILE" ]; then
      cat "$NOTES_FILE"
    else
      printf '_No user-facing changes; see the [GitHub Release](%s/releases/tag/%s) for the full commit log._\n' \
        "$repo_url" "$VERSION"
    fi
  } >"$CHANGELOG_SECTION_FILE"
fi

if [ -s "$NOTES_FILE" ]; then
  echo "Wrote $NOTES_FILE from $count PR(s) in range $range." >&2
else
  : >"$NOTES_FILE" # ensure a 0-byte file so the workflow's -s check is definitive
  echo "No user-facing PRs in range $range; $NOTES_FILE is empty (GoReleaser fallback)." >&2
fi
