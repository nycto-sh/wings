#!/usr/bin/env bash
#
# Changes the upstream module path, github.com/pterodactyl/wings, back to this
# fork's, github.com/nycto-sh/wings, after upstream changes have been brought
# in. Only the files that use it as a module path are changed: Go code, go.mod,
# the Makefile, the Dockerfile and the CI workflows. Links to the upstream
# repository, such as https://github.com/pterodactyl/wings, are kept.
#
#   scripts/fix-module-path.sh
#
# See docs/upstream-sync.md.

set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

changed=()
while IFS= read -r f; do
	if ! grep -q 'github\.com/pterodactyl/wings' "$f"; then
		continue
	fi
	before="$(git hash-object "$f")"
	# Only a path that does not follow a "/", so that https:// links stay.
	sed -i -e 's#\([^/]\)github\.com/pterodactyl/wings#\1github.com/nycto-sh/wings#g' \
		-e 's#^github\.com/pterodactyl/wings#github.com/nycto-sh/wings#' "$f"
	if [ "$(git hash-object "$f")" != "$before" ]; then
		changed+=("$f")
	fi
done < <(git ls-files '*.go' go.mod Makefile Dockerfile '.github/workflows/*.yaml')

if [ "${#changed[@]}" -eq 0 ]; then
	echo "Nothing refers to the upstream module path."
else
	printf 'Changed the module path in:\n'
	printf '  %s\n' "${changed[@]}"

	# The new import paths sort differently, so put the imports back in order.
	if command -v gofmt >/dev/null 2>&1; then
		go_files=()
		for f in "${changed[@]}"; do
			case "$f" in *.go) go_files+=("$f") ;; esac
		done
		if [ "${#go_files[@]}" -gt 0 ]; then
			gofmt -w "${go_files[@]}"
		fi
	else
		echo "gofmt was not found: run it on the changed .go files before committing."
	fi
fi

remaining="$(git grep -n 'github\.com/pterodactyl/wings' -- '*.go' go.mod Makefile Dockerfile '.github/workflows/*.yaml' | grep -v 'https://github\.com/pterodactyl/wings' || true)"
if [ -n "$remaining" ]; then
	echo "Still refers to the upstream module path, check these by hand:"
	echo "$remaining"
	exit 1
fi
