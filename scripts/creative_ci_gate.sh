#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

GO_TEST_PATTERN="${CREATIVE_GO_TEST_PATTERN:-Creative|Header|Video|Asset|Model|Router}"
GO_TEST_PACKAGES=(./relay/common ./relay/channel ./controller ./service ./router)

printf 'creative_ci_gate=go_tests pattern=%s\n' "$GO_TEST_PATTERN"
go test "${GO_TEST_PACKAGES[@]}" -run "$GO_TEST_PATTERN"

check_dist_tree() {
  local label="$1" dir="$2"
  if [[ ! -d "$dir" ]]; then
    printf 'ERROR: %s dist directory missing: %s\n' "$label" "$dir" >&2
    exit 1
  fi
  if [[ ! -f "$dir/index.html" ]]; then
    printf 'ERROR: %s index.html missing: %s/index.html\n' "$label" "$dir" >&2
    exit 1
  fi
}

check_index_refs() {
  local label="$1" file="$2"
  if grep -Eq 'src="\.\/assets/|href="\.\/assets/' "$file"; then
    printf 'ERROR: %s contains ./assets entry refs; rebuild OpenTU with VITE_BASE_URL=/creative/\n' "$label" >&2
    exit 1
  fi
  if grep -Eq 'src="/assets/|href="/assets/' "$file"; then
    printf 'ERROR: %s contains root /assets entry refs; rebuild OpenTU with VITE_BASE_URL=/creative/\n' "$label" >&2
    exit 1
  fi
  if ! grep -Eq 'src="/creative/assets/|href="/creative/assets/' "$file"; then
    printf 'ERROR: %s does not reference /creative/assets/ entry files\n' "$label" >&2
    exit 1
  fi
}

check_no_sourcemaps() {
  local findings
  findings="$(mktemp)"
  find web/creative/dist router/web/creative/dist -type f -name '*.map' -print >"$findings"
  if [[ -s "$findings" ]]; then
    cat "$findings" >&2
    rm -f "$findings"
    printf 'ERROR: Creative embedded dist contains sourcemap files.\n' >&2
    exit 1
  fi
  rm -f "$findings"

  findings="$(mktemp)"
  if grep -R -n -E 'sourceMappingURL=' web/creative/dist router/web/creative/dist >"$findings"; then
    cat "$findings" >&2
    rm -f "$findings"
    printf 'ERROR: Creative embedded dist contains sourceMappingURL references.\n' >&2
    exit 1
  fi
  rm -f "$findings"
}

check_no_debug_artifacts() {
  local findings
  findings="$(mktemp)"
  find web/creative/dist router/web/creative/dist -type f \( -name 'stats.html' -o -name 'sw-debug.html' -o -name 'cdn-debug.html' \) -print >"$findings"
  if [[ -s "$findings" ]]; then
    cat "$findings" >&2
    rm -f "$findings"
    printf 'ERROR: Creative embedded dist contains production-forbidden debug analysis artifacts.\n' >&2
    exit 1
  fi
  rm -f "$findings"

  findings="$(mktemp)"
  if grep -R -n -E '/mnt/[^[:space:]'\''"<>]*|node_modules/\\.pnpm|packages/drawnix/src|sw-debug\\.html|cdn-debug\\.html|menu\\.debugPanel' web/creative/dist router/web/creative/dist >"$findings"; then
    cat "$findings" >&2
    rm -f "$findings"
    printf 'ERROR: Creative embedded dist leaks build paths, source module graph markers, or production-forbidden debug entry references.\n' >&2
    exit 1
  fi
  rm -f "$findings"
}

check_version_provenance() {
  python3 - <<'PY'
import json
import re
from pathlib import Path

commit_re = re.compile(r'^[0-9a-f]{7,64}$', re.I)
paths = [
    ('web', Path('web/creative/dist/version.json')),
    ('router', Path('router/web/creative/dist/version.json')),
]
payloads = []
for label, path in paths:
    if not path.is_file():
        raise SystemExit(f'ERROR: {label} version.json missing: {path}')
    try:
        payload = json.loads(path.read_text(encoding='utf-8'))
    except json.JSONDecodeError as exc:
        raise SystemExit(f'ERROR: {label} version.json is invalid JSON: {exc}') from exc
    version = str(payload.get('version') or '').strip()
    build_time = str(payload.get('buildTime') or '').strip()
    git_commit = str(payload.get('gitCommit') or '').strip()
    if not version:
        raise SystemExit(f'ERROR: {label} version.json has empty version')
    if not build_time:
        raise SystemExit(f'ERROR: {label} version.json has empty buildTime')
    if git_commit.lower() == 'unknown' or not commit_re.match(git_commit):
        raise SystemExit(
            f'ERROR: {label} version.json has invalid gitCommit {git_commit!r}; '
            'embedded provenance must be a concrete git commit.'
        )
    payloads.append((label, payload))

baseline_label, baseline = payloads[0]
for label, payload in payloads[1:]:
    if payload != baseline:
        raise SystemExit(
            f'ERROR: {label} version.json differs from {baseline_label}; '
            'embedded Creative dist provenance must be identical.'
        )
print(f"creative_ci_gate=version_provenance version={baseline['version']} gitCommit={baseline['gitCommit']}")
PY
}

check_git_release_tree() {
  if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    printf 'creative_ci_gate=git_release_tree skipped reason=not_git_work_tree\n'
    return
  fi

  local untracked deleted
  untracked="$(git ls-files --others --exclude-standard -- scripts/creative_ci_gate.sh web/creative/dist router/web/creative/dist)"
  if [[ -n "$untracked" ]]; then
    printf '%s\n' "$untracked" >&2
    printf 'ERROR: Creative release tree has untracked required files; run git add for gate script and embedded dist assets.\n' >&2
    exit 1
  fi

  deleted="$(git ls-files --deleted -- scripts/creative_ci_gate.sh web/creative/dist router/web/creative/dist)"
  if [[ -n "$deleted" ]]; then
    printf '%s\n' "$deleted" >&2
    printf 'ERROR: Creative release tree has deleted tracked files not reflected in the git index; run git add -A for embedded dist assets.\n' >&2
    exit 1
  fi

  if ! git ls-files --error-unmatch scripts/creative_ci_gate.sh >/dev/null 2>&1; then
    printf 'ERROR: scripts/creative_ci_gate.sh is not tracked by git; release workflows cannot reproduce this gate.\n' >&2
    exit 1
  fi
}

check_dist_tree web web/creative/dist
check_dist_tree router router/web/creative/dist
printf 'creative_ci_gate=git_release_tree\n'
check_git_release_tree
printf 'creative_ci_gate=dist_diff\n'
diff -qr web/creative/dist router/web/creative/dist >/tmp/creative-dist-diff.txt || {
  cat /tmp/creative-dist-diff.txt >&2
  printf 'ERROR: Creative dist trees differ; run the cross-repo sync gate before release.\n' >&2
  exit 1
}
check_index_refs web web/creative/dist/index.html
check_index_refs router router/web/creative/dist/index.html
printf 'creative_ci_gate=no_sourcemaps\n'
check_no_sourcemaps
printf 'creative_ci_gate=no_debug_artifacts\n'
check_no_debug_artifacts
check_version_provenance

if [[ -n "${CREATIVE_EMBEDDED_SMOKE_URL:-}" ]]; then
  if [[ -z "${CREATIVE_EMBEDDED_SMOKE_CMD:-}" ]]; then
    printf 'ERROR: CREATIVE_EMBEDDED_SMOKE_URL is set but CREATIVE_EMBEDDED_SMOKE_CMD is empty.\n' >&2
    printf 'Set CREATIVE_EMBEDDED_SMOKE_CMD to the approved Playwright/browser smoke command for this candidate server.\n' >&2
    exit 1
  fi
  printf 'creative_ci_gate=embedded_smoke url=%s\n' "$CREATIVE_EMBEDDED_SMOKE_URL"
  bash -lc "$CREATIVE_EMBEDDED_SMOKE_CMD"
else
  printf 'creative_ci_gate=embedded_smoke skipped reason=no_candidate_server_url\n'
fi

printf 'creative_ci_gate=pass\n'
