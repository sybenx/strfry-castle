#!/usr/bin/env bash
# uninstall.sh — reverses what install.sh set up.
#
#   curl -fsSL https://raw.githubusercontent.com/sybenx/castle-for-strfry-experiment/main/uninstall.sh | bash
#
# Same rule as install.sh (see DECISIONS.md): this PRINTS the lines to
# remove from your compose file rather than editing it — an uninstaller
# that edits an unknown stack is exactly the kind of thing that bricks a
# relay. It removes what install.sh actually left on disk (the pulled
# image, the .env it wrote) and leaves castle-state alone by default,
# since that volume holds the ledger — the only durable record of your
# invite tree and elevations.
set -euo pipefail

IMAGE="ghcr.io/sybenx/castle-steward"
ENV_FILE="${CASTLE_ENV_FILE:-.env}"

log()  { printf '==> %s\n' "$*"; }

ask() {
  local prompt="$1" reply
  [ -e /dev/tty ] || { echo ""; return; }
  printf '%s' "$prompt" > /dev/tty
  read -r reply < /dev/tty
  printf '%s' "$reply"
}

# Prefer the actual locally pulled tag over a generic placeholder, when
# there's exactly one — it's what install.sh most likely wrote into this
# host's compose file, so echoing it back is a more useful diff target
# than a wildcard. Ambiguous or absent falls back to "...".
SNIPPET_TAG="..."
if command -v docker >/dev/null 2>&1; then
  TAGS=$(docker images "$IMAGE" --format '{{.Tag}}' 2>/dev/null || true)
  if [ -n "$TAGS" ] && [ "$(printf '%s\n' "$TAGS" | wc -l)" -eq 1 ]; then
    SNIPPET_TAG="$TAGS"
  fi
fi

cat <<EOF
------------------------------------------------------------------------------
Remove this service (and the castle-state volume entry, if you added it)
from your docker-compose.yml, then run \`docker compose up -d\` to apply:

  steward:
    image: $IMAGE:$SNIPPET_TAG
    depends_on:
      - <your strfry container>
    env_file: .env
    volumes:
      - castle-state:/state
      - /var/run/docker.sock:/var/run/docker.sock
    ports:
      - "8787:8787"

Also remove any reverse-proxy blocks routing to steward:8787 that
install.sh printed for you.
------------------------------------------------------------------------------
EOF

if command -v docker >/dev/null 2>&1; then
  if docker image inspect "$IMAGE" >/dev/null 2>&1 || docker images -q "$IMAGE" 2>/dev/null | grep -q .; then
    log "removing local image(s) for $IMAGE"
    docker images "$IMAGE" --format '{{.Repository}}:{{.Tag}}' | xargs -r -n1 docker rmi >/dev/null 2>&1 || true
  else
    log "no local $IMAGE image found (nothing to remove)"
  fi
else
  log "docker not found on PATH — skipping image removal"
fi

if [ -f "$ENV_FILE" ]; then
  REPLY=$(ask "remove $ENV_FILE (contains OWNER_PUBKEY and your raid settings)? [y/N] ")
  case "$REPLY" in
    y|Y|yes|Yes) rm -f "$ENV_FILE"; log "removed $ENV_FILE" ;;
    *) log "leaving $ENV_FILE in place" ;;
  esac
fi

cat <<'EOF'

------------------------------------------------------------------------------
The castle-state docker volume has been left untouched. It holds
ledger.jsonl — the only durable record of your invite tree, follows
snapshot, and elevations (favorites and wards). Nostr events themselves
age off relays; this volume is not recoverable from strfry after deletion.

To delete it anyway:

  docker volume rm castle-state

------------------------------------------------------------------------------
EOF
