#!/usr/bin/env fish
#
# release.fish — run checks, commit version metadata, build, and publish a GitHub release for test-gdoc.
#
# Optional workflow before running:
#   1. Update CHANGES.md with notes for the release (under the top-most `## [x.y.z]` section).
#   2. Bump versions if desired: `npm version patch|minor|major --no-git-tag-version`
#   3. Commit all code changes so HEAD matches the built plugin artifacts.
#   4. Run `./release.fish -n` to preview, then `./release.fish` to publish.
#
# Usage:
#   ./release.fish              run checks, commit release files, push, build, publish release
#   ./release.fish -y|--yes     skip the confirmation prompt
#   ./release.fish -n|--dry-run show the release plan without making changes
#   ./release.fish -h|--help    show this help text

function _die
    set_color red; echo "release: $argv" >&2; set_color normal
    exit 1
end

function _step
    set_color --bold cyan; echo "▶ $argv"; set_color normal
end

# ── Arguments ────────────────────────────────────────────────────────────────
set -l assume_yes 0
set -l dry_run 0
for arg in $argv
    switch $arg
        case -y --yes
            set assume_yes 1
        case -n --dry-run
            set dry_run 1
        case -h --help
            echo "Usage: ./release.fish [-y|--yes] [-n|--dry-run] [-h|--help]"
            exit 0
        case '*'
            _die "unknown argument: $arg (use -y/--yes, -n/--dry-run, or -h/--help)"
    end
end

# ── Preflight ────────────────────────────────────────────────────────────────
for bin in git node npm go gh
    type -q $bin; or _die "$bin not found in PATH"
end

set -l repo (git rev-parse --show-toplevel 2>/dev/null); or _die "not inside a git repository"
cd $repo; or _die "cannot cd to repo root: $repo"

for req in package.json manifest.json versions.json CHANGES.md
    test -f $req; or _die "missing required file: $req"
end

set -l relver (node -p "require('./package.json').version" 2>/dev/null)
test $status -eq 0 -a -n "$relver"; or _die "cannot read version from package.json"

set -l manifest_ver (node -p "require('./manifest.json').version" 2>/dev/null)
set -l manifest_needs_sync 0
if test "$manifest_ver" != "$relver"
    set manifest_needs_sync 1
end

set -l branch (git rev-parse --abbrev-ref HEAD)

# Refuse to clobber an existing release or tag for this version.
if gh release view $relver >/dev/null 2>&1
    _die "GitHub release '$relver' already exists — bump package.json version first"
end
if git rev-parse -q --verify "refs/tags/$relver" >/dev/null 2>&1
    _die "local git tag '$relver' already exists — bump package.json version first"
end

# Ensure all non-release files are committed so the release tag matches built artifacts.
set -l dirty_other
for line in (git status --porcelain)
    set -l file (string sub -s 4 -- $line)
    # Handle renames ("old -> new") by checking the destination path.
    if string match -q '* -> *' -- $file
        set file (string replace -r '.* -> ' '' -- $file)
    end
    switch $file
        case package.json manifest.json versions.json CHANGES.md
            # Allowed release metadata files
        case '*'
            set -a dirty_other $file
    end
end

if test (count $dirty_other) -gt 0 -a $dry_run -eq 0
    _die "uncommitted changes outside release files ($dirty_other) — commit or stash them first"
end

# ── Locate the top-most release header in CHANGES.md ─────────────────────────
set -l hdr (grep -nE '^## \[[0-9][^]]*\]' CHANGES.md | head -n1)
test -n "$hdr"; or _die "no release header ('## [x.y.z]') found in CHANGES.md"
set -l hdr_lineno (string split -f1 ':' -- $hdr)
set -l hdr_version (string match -rg '^[0-9]+:## \[([^]]+)\]' -- $hdr)

set -l header_needs_fix 0
test "$hdr_version" != "$relver"; and set header_needs_fix 1

set -l notes_file (mktemp)
awk '
    /^## / { hdr++; if (hdr == 1) next; if (hdr == 2) exit }
    hdr == 1 { print }
' CHANGES.md | sed -e '/./,$!d' | tac | sed -e '/./,$!d' | tac >$notes_file
test -s $notes_file; or echo "release: warning — top CHANGES.md section is empty" >&2

set -l artifacts main.js manifest.json
if test -f styles.css
    set -a artifacts styles.css
end

# ── Plan ─────────────────────────────────────────────────────────────────────
echo
_step "Release plan"
echo "  version  : $relver"
echo "  branch   : $branch → push to origin"
echo "  checks   : npm run test:go && npm test && npm run build && npm run lint"
if test $manifest_needs_sync -eq 1
    echo "  sync     : manifest.json ($manifest_ver) & versions.json → $relver via version-bump.mjs"
end
echo "  commit   : \"release v$relver\" (package.json manifest.json versions.json CHANGES.md)"
if test $header_needs_fix -eq 1
    echo "  CHANGES  : retitle top header [$hdr_version] → [$relver]"
else
    echo "  CHANGES  : top header already [$relver]"
end
echo "  artifacts: $artifacts"
echo "  notes    : top CHANGES.md section ↓"
sed 's/^/             │ /' $notes_file
if test (count $dirty_other) -gt 0
    set_color yellow
    echo "  warning  : uncommitted non-release changes exist (dry-run only): $dirty_other"
    set_color normal
end
echo

if test $dry_run -eq 1
    set_color yellow; echo "dry run — no changes made"; set_color normal
    rm -f $notes_file
    exit 0
end

if test $assume_yes -eq 0
    read -l -P "Proceed with checks, commit, push, build, and GitHub release? [y/N] " ans
    if not string match -qir '^y(es)?$' -- $ans
        rm -f $notes_file
        _die "aborted by user"
    end
end

# ── 1. Sync manifest.json / versions.json / CHANGES.md if needed ─────────────
if test $manifest_needs_sync -eq 1
    _step "Syncing manifest.json and versions.json to $relver"
    env npm_package_version=$relver node version-bump.mjs; or begin
        rm -f $notes_file; _die "version-bump.mjs failed"
    end
end

if test $header_needs_fix -eq 1
    _step "Retitling CHANGES.md top header → [$relver]"
    sed -i "$hdr_lineno s/^## \[.*\]/## [$relver]/" CHANGES.md; or begin
        rm -f $notes_file; _die "failed to edit CHANGES.md"
    end
end

# ── 2. Verify tests, build, and lint before committing/tagging ───────────────
_step "Running Go unit tests (npm run test:go)"
npm run test:go; or begin
    rm -f $notes_file; _die "Go tests failed"
end

_step "Running TypeScript unit tests (npm test)"
npm test; or begin
    rm -f $notes_file; _die "Vitest suite failed"
end

_step "Building release artifacts (npm run build)"
npm run build; or begin
    rm -f $notes_file; _die "build failed"
end

_step "Linting (npm run lint)"
npm run lint; or begin
    rm -f $notes_file; _die "lint failed"
end

for f in $artifacts
    test -f $f; or begin
        rm -f $notes_file; _die "missing release artifact: $f"
    end
end

# ── 3. Commit release metadata ───────────────────────────────────────────────
_step "Committing release v$relver"
git add package.json manifest.json versions.json CHANGES.md; or begin
    rm -f $notes_file; _die "git add failed"
end
if git diff --cached --quiet
    echo "release: nothing staged to commit — assuming HEAD is already tagged-ready"
else
    git commit -m "release v$relver"; or begin
        rm -f $notes_file; _die "git commit failed"
    end
end

# ── 4. Push branch ───────────────────────────────────────────────────────────
_step "Pushing $branch to origin"
git push origin $branch; or begin
    rm -f $notes_file; _die "git push failed"
end

# ── 5. Create published GitHub release ───────────────────────────────────────
set -l target (git rev-parse HEAD)
_step "Creating GitHub release $relver"
if not gh release create $relver $artifacts \
        --target $target \
        --title "$relver" \
        --notes-file $notes_file
    rm -f $notes_file
    _die "gh release create failed (commit/push succeeded — rerun once resolved)"
end

set -l repo_url (gh repo view --json url -q .url 2>/dev/null)
rm -f $notes_file
set_color --bold green; echo "✔ Released $relver"; set_color normal
if test -n "$repo_url"
    echo "  $repo_url/releases/tag/$relver"
end
