#!/usr/bin/env bash
#
# Shows what has changed in upstream Pterodactyl Wings since it was last
# brought into this fork, and which of those changes touch security-sensitive
# code. It only reads from upstream: nothing is pushed there.
#
#   scripts/upstream-status.sh
#
# See docs/upstream-sync.md.

set -euo pipefail

UPSTREAM_URL="https://github.com/pterodactyl/wings.git"
BRANCH="develop"

cd "$(git rev-parse --show-toplevel)"

if ! git remote get-url upstream >/dev/null 2>&1; then
	git remote add upstream "$UPSTREAM_URL"
	# Make pushing to upstream impossible by accident.
	git remote set-url --push upstream DISABLED
fi
git fetch --quiet --no-tags upstream "$BRANCH"

latest="$(git ls-remote --tags --refs upstream 'v*' | awk -F/ '{print $3}' | sort -V | tail -n 1)"
echo "Upstream $BRANCH:        $(git log -1 --format='%h %ad %s' --date=short "upstream/$BRANCH")"
echo "Latest upstream release: ${latest:-unknown}"
echo "This fork is based on:   $(git log -1 --format='%h %ad' --date=short "$(git merge-base HEAD "upstream/$BRANCH")")"
echo

count="$(git rev-list --count "HEAD..upstream/$BRANCH")"
if [ "$count" -eq 0 ]; then
	echo "The fork has every upstream change."
	exit 0
fi

echo "$count upstream commit(s) are not in the fork yet:"
git log --format='  %h %ad %an: %s' --date=short "HEAD..upstream/$BRANCH"
echo

echo "Files they change:"
git diff --stat "HEAD...upstream/$BRANCH" | sed 's/^/  /'
echo

# The areas CLAUDE.md asks to review with extra care.
sensitive="$(git diff --name-only "HEAD...upstream/$BRANCH" |
	grep -E '^(server/filesystem|internal/ufs|router/tokens|router/middleware|router/websocket|sftp|environment/docker|server/backup|server/transfer|router/downloader)/|^config/config\.go$' || true)"
if [ -n "$sensitive" ]; then
	echo "Security-sensitive files changed, review these with extra care:"
	while IFS= read -r f; do
		echo "  $f"
	done <<<"$sensitive"
	echo
fi

echo "To bring them in, follow docs/upstream-sync.md."
