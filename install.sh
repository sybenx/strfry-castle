#!/usr/bin/env bash
# install.sh — the Castle, one-line installer.
#
#   curl -fsSL https://raw.githubusercontent.com/sybenx/castle-for-strfry-experiment/main/install.sh | bash
#
# What this does, and does NOT do (see CLAUDE.md "Distribution" and
# DECISIONS.md — install.sh editing strfry.conf / compose / proxy configs
# is rejected outright):
#   - detects your OS/arch and a running strfry container (asks you to
#     confirm the guess)
#   - downloads the matching steward release asset and verifies its
#     sha256 against the release's published checksums
#   - creates the castle-state docker volume (no-op if it already exists)
#   - writes ./.env with your OWNER_PUBKEY and the documented defaults
#     (RAID_DRY_RUN=true, RAID_CRON empty — manual, dry-run raids until
#     you arm it yourself)
#   - PRINTS the docker-compose service snippet and reverse-proxy config
#     for you to paste in. It never edits an existing stack's files —
#     auto-editing an unknown Umbrel/Portainer setup is how installs
#     brick relays.
#
# Safe to re-run: docker volume create is idempotent, and an existing
# .env with OWNER_PUBKEY already set is left alone rather than clobbered.
set -euo pipefail

REPO="sybenx/castle-for-strfry-experiment"
IMAGE="ghcr.io/sybenx/castle-steward"
ENV_FILE="${CASTLE_ENV_FILE:-.env}"

log()  { printf '==> %s\n' "$*"; }
warn() { printf 'install.sh: warning: %s\n' "$*" >&2; }
die()  { printf 'install.sh: error: %s\n' "$*" >&2; exit 1; }

# ask prints its prompt and reads a reply from the controlling terminal,
# not stdin — required because this script is normally run as
# `curl ... | bash`, which leaves stdin attached to curl's pipe.
ask() {
  local prompt="$1" reply
  [ -e /dev/tty ] || die "no controlling terminal to prompt on; download and run install.sh directly instead of piping into a non-interactive shell"
  printf '%s' "$prompt" > /dev/tty
  read -r reply < /dev/tty
  printf '%s' "$reply"
}

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

# ---------------------------------------------------------------------------
# npub -> hex (bech32), self-contained so this script has no dependency
# beyond curl/tar/docker. Verified against nak-generated keypairs.
BECH32_CHARSET="qpzry9x8gf2tvdw0s3jn54khce6mua7l"

bech32_charset_index() {
  local c="$1" i
  for (( i=0; i<${#BECH32_CHARSET}; i++ )); do
    [[ "${BECH32_CHARSET:$i:1}" == "$c" ]] && { echo "$i"; return 0; }
  done
  return 1
}

bech32_hrp_expand() {
  local hrp="$1" out=() i
  for (( i=0; i<${#hrp}; i++ )); do out+=( $(( $(printf '%d' "'${hrp:$i:1}") >> 5 )) ); done
  out+=( 0 )
  for (( i=0; i<${#hrp}; i++ )); do out+=( $(( $(printf '%d' "'${hrp:$i:1}") & 31 )) ); done
  echo "${out[@]}"
}

bech32_polymod() {
  local -a GEN=(0x3b6a57b2 0x26508e6d 0x1ea119fa 0x3d4233dd 0x2a1462b3)
  local chk=1 v top i
  for v in "$@"; do
    top=$(( chk >> 25 ))
    chk=$(( ( (chk & 0x1ffffff) << 5 ) ^ v ))
    for i in 0 1 2 3 4; do
      (( (top >> i) & 1 )) && chk=$(( chk ^ GEN[i] ))
    done
  done
  echo "$chk"
}

# npub_to_hex NPUB -> prints 64-char hex pubkey, or fails on bad checksum
npub_to_hex() {
  local input lower upper hrp data_part
  input="$1"
  lower=$(tr 'A-Z' 'a-z' <<<"$input")
  upper=$(tr 'a-z' 'A-Z' <<<"$input")
  [[ "$input" == "$lower" || "$input" == "$upper" ]] || { echo "mixed-case bech32 string" >&2; return 1; }
  input="$lower"
  [[ "$input" == *1* ]] || { echo "not a bech32 string (no separator)" >&2; return 1; }
  hrp="${input%1*}"
  data_part="${input##*1}"
  [[ "$hrp" == "npub" ]] || { echo "expected an npub1... string, got hrp=$hrp" >&2; return 1; }

  local -a vals=() hrpvals=() full=()
  local i c idx
  for (( i=0; i<${#data_part}; i++ )); do
    c="${data_part:$i:1}"
    idx=$(bech32_charset_index "$c") || { echo "invalid bech32 character: $c" >&2; return 1; }
    vals+=( "$idx" )
  done
  hrpvals=( $(bech32_hrp_expand "$hrp") )
  full=( "${hrpvals[@]}" "${vals[@]}" )
  [[ "$(bech32_polymod "${full[@]}")" == "1" ]] || { echo "bad npub checksum" >&2; return 1; }

  local n=${#vals[@]}
  local -a payload=( "${vals[@]:0:$((n-6))}" )
  local acc=0 bits=0 out="" v
  for v in "${payload[@]}"; do
    acc=$(( (acc << 5) | v ))
    bits=$(( bits + 5 ))
    while (( bits >= 8 )); do
      bits=$(( bits - 8 ))
      out+=$(printf '%02x' $(( (acc >> bits) & 255 )) )
    done
  done
  [[ ${#out} -eq 64 ]] || { echo "decoded to ${#out} hex chars, expected 64" >&2; return 1; }
  echo "$out"
}

# ---------------------------------------------------------------------------
for cmd in curl docker tar; do
  command -v "$cmd" >/dev/null 2>&1 || die "$cmd is required but not found on PATH"
done

case "$(uname -s)" in
  Linux) ;;
  *) die "the Castle's steward container needs a Linux docker host (it shells out to docker exec against your strfry container via a mounted /var/run/docker.sock); $(uname -s) is not supported" ;;
esac

case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) die "unsupported architecture: $(uname -m) (steward ships linux/amd64 and linux/arm64 only)" ;;
esac
log "detected linux/$ARCH"

docker info >/dev/null 2>&1 || die "docker is installed but not usable (is the daemon running, and do you have permission to talk to it?)"

# --- resolve + verify the release -------------------------------------------
VERSION="${CASTLE_VERSION:-}"
if [ -z "$VERSION" ]; then
  log "looking up the latest release of $REPO"
  VERSION=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
    | grep -m1 '"tag_name"' | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')
  [ -n "$VERSION" ] || die "could not determine the latest release tag; set CASTLE_VERSION=vX.Y.Z and re-run"
fi
log "installing $VERSION"

WORKDIR=$(mktemp -d)
trap 'rm -rf "$WORKDIR"' EXIT

ASSET="steward-linux-$ARCH.tar.gz"
BASE_URL="https://github.com/$REPO/releases/download/$VERSION"
log "downloading $ASSET"
curl -fsSL -o "$WORKDIR/$ASSET" "$BASE_URL/$ASSET"
curl -fsSL -o "$WORKDIR/checksums.txt" "$BASE_URL/checksums.txt"

EXPECTED=$(grep "$ASSET" "$WORKDIR/checksums.txt" | awk '{print $1}')
[ -n "$EXPECTED" ] || die "checksums.txt has no entry for $ASSET"
ACTUAL=$(sha256_of "$WORKDIR/$ASSET")
[ "$EXPECTED" = "$ACTUAL" ] || die "checksum mismatch for $ASSET: expected $EXPECTED, got $ACTUAL"
log "checksum verified ($ACTUAL)"
tar -xzf "$WORKDIR/$ASSET" -C "$WORKDIR"
"$WORKDIR/steward" --version >/dev/null 2>&1 || true

# --- detect the strfry container --------------------------------------------
CANDIDATES=$(docker ps --format '{{.Names}}\t{{.Image}}' | grep -i strfry || true)
CONTAINER=""
if [ -n "$CANDIDATES" ] && [ "$(printf '%s\n' "$CANDIDATES" | wc -l)" -eq 1 ]; then
  GUESS=$(printf '%s\n' "$CANDIDATES" | cut -f1)
  REPLY=$(ask "found a running strfry container named '$GUESS' — use it? [Y/n] ")
  case "$REPLY" in
    ""|y|Y|yes|Yes) CONTAINER="$GUESS" ;;
  esac
elif [ -n "$CANDIDATES" ]; then
  echo "multiple candidate containers found:" > /dev/tty
  printf '%s\n' "$CANDIDATES" > /dev/tty
fi
if [ -z "$CONTAINER" ]; then
  CONTAINER=$(ask "container name of your running strfry instance: ")
fi
[ -n "$CONTAINER" ] || die "a strfry container name is required"
docker inspect "$CONTAINER" >/dev/null 2>&1 || warn "no container named '$CONTAINER' is currently running — steward will retry once it exists"
log "using strfry container: $CONTAINER"

# --- OWNER_PUBKEY ------------------------------------------------------------
OWNER_HEX=""
if [ -f "$ENV_FILE" ]; then
  OWNER_HEX=$(grep -E '^OWNER_PUBKEY=' "$ENV_FILE" 2>/dev/null | head -1 | cut -d= -f2- || true)
fi
if [ -n "$OWNER_HEX" ]; then
  log "found existing $ENV_FILE with OWNER_PUBKEY already set — leaving it as configured"
  log "(delete $ENV_FILE, or set OWNER_PUBKEY there yourself, to change the Lord)"
else
  while [ -z "$OWNER_HEX" ]; do
    RAW=$(ask "the Lord's pubkey (npub or hex): ")
    if [[ "$RAW" =~ ^npub1[a-z0-9]+$ ]]; then
      OWNER_HEX=$(npub_to_hex "$RAW") || { echo "that npub didn't decode; try again" > /dev/tty; OWNER_HEX=""; continue; }
    elif [[ "$RAW" =~ ^[0-9a-fA-F]{64}$ ]]; then
      OWNER_HEX=$(tr 'A-Z' 'a-z' <<<"$RAW")
    else
      echo "not a valid npub or 64-char hex pubkey; try again" > /dev/tty
    fi
  done
fi

# --- state volume + .env -----------------------------------------------------
docker volume create castle-state >/dev/null
log "castle-state docker volume ready"

if [ ! -f "$ENV_FILE" ] || [ -z "$(grep -E '^OWNER_PUBKEY=' "$ENV_FILE" 2>/dev/null | cut -d= -f2-)" ]; then
  cat > "$ENV_FILE" <<EOF
# Written by install.sh ($VERSION). See CLAUDE.md, Component 2, for what
# each of these does.
OWNER_PUBKEY=$OWNER_HEX
STRFRY_CONTAINER=$CONTAINER
PUBLIC_RELAYS=
OUTER_TTL_DAYS=30
CYCLE_MINUTES=10
RAID_CRON=
RAID_DRY_RUN=true
MAX_INVITES=5
MAX_DEPTH=4
LISTEN=:8787
EOF
  log "wrote $ENV_FILE"
fi

# --- print, never apply -------------------------------------------------------
cat <<EOF

------------------------------------------------------------------------------
Add this service to your existing strfry docker-compose.yml (steward reaches
strfry only via '$CONTAINER' on the compose network + the docker.sock mount
below — no strfry.conf changes, no plugin volume):

  steward:
    image: $IMAGE:$VERSION
    depends_on:
      - $CONTAINER
    env_file: $ENV_FILE
    volumes:
      - castle-state:/state
      - /var/run/docker.sock:/var/run/docker.sock
    ports:
      - "8787:8787"

volumes:
  castle-state:
    name: castle-state

Then: docker compose up -d steward

------------------------------------------------------------------------------
Reverse proxy (route WebSocket upgrades + NIP-11 requests to strfry,
everything else to steward; forward a real client IP). Caddy:

  your-relay-domain.example {
      @websocket {
          header Connection *Upgrade*
          header Upgrade websocket
      }
      @nip11 {
          header Accept application/nostr+json
      }
      handle @websocket { reverse_proxy $CONTAINER:7777 }
      handle @nip11      { reverse_proxy $CONTAINER:7777 }
      handle             { reverse_proxy steward:8787 }
  }

nginx: see deploy/nginx.conf in the repo for the equivalent block.

------------------------------------------------------------------------------
IMPORTANT — the steward container mounts /var/run/docker.sock so it can run
'docker exec' against strfry. That socket is root-equivalent on this host,
and steward is internet-facing. Accepted for one-DB-owner simplicity; if
that trade doesn't work for you, put a socket proxy (e.g.
tecnativa/docker-socket-proxy, limited to exec) in front of it instead.

RAID_DRY_RUN=true and RAID_CRON is empty by default: nothing gets deleted
and nothing runs on a schedule until you review a dry-run raid's log and
arm it yourself.

The strfry writePolicy plugin slot is untouched — steward never gates
writes. Plug in any stock strfry spam-filter plugin there if you want one.
------------------------------------------------------------------------------
EOF
