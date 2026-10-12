#!/usr/bin/env bash
# frontend-shared/ is the single source of what both frontends (React and Svelte) have in common:
#   src/  the API client and generated types, formatting, statistics, filters, colours, alert wording, the NDT7 summary, push and
#         install logic, the service worker, and their unit tests. Copied to <app>/src/shared/
#   e2e/  the browser tests (Playwright). Both apps must pass the very same ones. Copied to <app>/e2e/
#   public/  icons and the theme script, copied to <app>/public/
#   docker/  the nginx configuration of the web image, copied to <app>/docker/
# Each app builds from its own folder (Docker builds see nothing outside it), so this copies the shared code into place.
# Edit frontend-shared/, never the copies.
#
#   scripts/sync-shared.sh           copy into every app that exists
#   scripts/sync-shared.sh --check   fail if a copy differs from the source (use before committing and in CI)
set -euo pipefail
cd "$(dirname "$0")/.."
mode="${1:-copy}"
status=0
same() { diff -r --exclude=README.md "$1" "$2" >/dev/null 2>&1; }
for app in frontend-react frontend-svelte; do
  [ -d "$app/src" ] || continue
  if [ "$mode" = "--check" ]; then
    for pair in "frontend-shared/src:$app/src/shared" "frontend-shared/e2e:$app/e2e" "frontend-shared/public:$app/public" "frontend-shared/docker:$app/docker"; do
      src="${pair%%:*}"; dest="${pair##*:}"
      if ! same "$src" "$dest"; then
        echo "$dest differs from $src (run scripts/sync-shared.sh)" >&2
        diff -r --exclude=README.md "$src" "$dest" 2>&1 | head -10 >&2 || true
        status=1
      fi
    done
  else
    rm -rf "$app/src/shared" "$app/e2e" "$app/docker"
    mkdir -p "$app/src/shared" "$app/e2e" "$app/public" "$app/docker"
    cp -R frontend-shared/src/. "$app/src/shared"/
    cp -R frontend-shared/e2e/. "$app/e2e"/
    cp -R frontend-shared/public/. "$app/public"/
    cp -R frontend-shared/docker/. "$app/docker"/
    cat > "$app/src/shared/README.md" <<'MD'
# Generated: do not edit here

This folder is a copy of `frontend-shared/src/` made by `scripts/sync-shared.sh`. Change the files in `frontend-shared/` and run
the script, or your edit will be overwritten (and `scripts/sync-shared.sh --check` will fail). The same goes for `../e2e/`.
MD
    echo "synced $app"
  fi
done
exit $status
