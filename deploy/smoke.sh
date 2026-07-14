#!/usr/bin/env bash
# `make smoke`: scratch strfry + fixture events via nak, exercising a real
# steward cycle (Phases 3a-3b) end to end. See PLAN.md's Phase 3a acceptance
# criterion: "cycle runs against a scratch strfry in docker-compose with
# fixture events published via nak; citizens.json/tree.json come out
# correct." and Phase 3b's: "stats.json validates against the schema from a
# live compose stack; ward count appears nowhere."
#
# Deliberately does NOT shell out to `docker compose` — it isn't guaranteed
# present (the Docker CLI and the compose plugin are installed separately;
# this environment has the former without the latter), and the scratch
# stack's needs (deterministic container/network names, precise mount
# ordering) are simpler to get right with plain `docker` commands anyway.
# docker-compose.yml remains the documented reference for wiring the Castle
# into a real Portainer stack; it is not this script's dependency.
#
# Colima/virtiofs gotcha (see .claude/notes/phase1.md): bind-mounting a
# single host FILE into a fresh container races virtiofs's file-visibility
# propagation and can silently vivify the mount point as an empty
# directory. Every bind mount below is a whole DIRECTORY (populated on the
# host before the mount happens), never a single file, to sidestep this
# entirely.
set -euo pipefail
cd "$(dirname "$0")"
REPO_ROOT="$(cd .. && pwd)"

for bin in docker nak jq; do
	if ! command -v "$bin" >/dev/null 2>&1; then
		echo "==> smoke: $bin is required but not found in PATH" >&2
		exit 1
	fi
done

NETWORK=castle-smoke
VOLUME_STATE=castle-state
VOLUME_DB=castle-smoke-db
API_BODY="" # set to a mktemp path once the API test section starts

case "$(uname -m)" in
	arm64|aarch64) ARCH=arm64 ;;
	x86_64|amd64)  ARCH=amd64 ;;
	*) echo "==> smoke: unsupported host arch $(uname -m)" >&2; exit 1 ;;
esac
BIN_DIR="$REPO_ROOT/bin/linux-$ARCH"
if [ ! -x "$BIN_DIR/steward" ]; then
	echo "==> smoke: $BIN_DIR/steward missing — run 'make build' first" >&2
	exit 1
fi

cleanup() {
	echo "==> smoke: tearing down"
	docker rm -f castle-smoke-steward castle-smoke-steward-api castle-smoke-strfry >/dev/null 2>&1 || true
	docker network rm "$NETWORK" >/dev/null 2>&1 || true
	docker volume rm "$VOLUME_STATE" "$VOLUME_DB" >/dev/null 2>&1 || true
	rm -f "$API_BODY" 2>/dev/null || true
}
trap cleanup EXIT
cleanup >/dev/null 2>&1 || true # in case a previous run left something behind

echo "==> smoke: bringing up scratch strfry"
docker network create "$NETWORK" >/dev/null
docker volume create "$VOLUME_STATE" >/dev/null
docker volume create "$VOLUME_DB" >/dev/null
docker run -d --name castle-smoke-strfry \
	--network "$NETWORK" \
	-p 7777:7777 \
	-v "$REPO_ROOT/deploy/smoke-conf":/config:ro \
	-v "$VOLUME_DB":/app/strfry-db \
	-e STRFRY_CONFIG=/config/strfry.conf \
	dockurr/strfry:latest >/dev/null

echo "==> smoke: waiting for strfry to accept connections"
up=false
for i in $(seq 1 30); do
	if nak req -l 1 ws://localhost:7777 >/dev/null 2>&1; then
		up=true
		break
	fi
	sleep 1
done
if [ "$up" != true ]; then
	echo "==> smoke: strfry never came up" >&2
	docker logs castle-smoke-strfry || true
	exit 1
fi
echo "==> smoke: strfry is up"

echo "==> smoke: generating fixture keys"
OWNER_SEC=$(nak key generate); OWNER_PUB=$(nak key public "$OWNER_SEC")
FOLLOW_SEC=$(nak key generate); FOLLOW_PUB=$(nak key public "$FOLLOW_SEC")
STRANGER_SEC=$(nak key generate); STRANGER_PUB=$(nak key public "$STRANGER_SEC")
echo "    owner=$OWNER_PUB follow=$FOLLOW_PUB stranger=$STRANGER_PUB"

echo "==> smoke: publishing fixture events"
# The Lord follows FOLLOW_PUB (kind 3), and each of the three keys posts one
# note — this exercises stats.json's the_lord/citizens/outer_lands counts
# (Phase 3b), not just citizens.json/tree.json (Phase 3a).
nak event -k 3 -p "$FOLLOW_PUB" --sec "$OWNER_SEC" -q ws://localhost:7777 >/dev/null
nak event -k 1 -c "the Lord speaks" --sec "$OWNER_SEC" -q ws://localhost:7777 >/dev/null
nak event -k 1 -c "a follow's note" --sec "$FOLLOW_SEC" -q ws://localhost:7777 >/dev/null
nak event -k 1 -c "a stranger's note" --sec "$STRANGER_SEC" -q ws://localhost:7777 >/dev/null

# STRFRY_CONTAINER doubles as the websocket hostname (ownRelayURL). Phase 3b's
# stats generation shells out via `docker exec` to run `strfry scan` inside
# castle-smoke-strfry, so this container needs the docker CLI + the host
# socket, unlike Phase 3a's plain alpine (which only did follows sync).
echo "==> smoke: running one steward cycle"
docker run -d --name castle-smoke-steward \
	--network "$NETWORK" \
	-v "$VOLUME_STATE":/state \
	-v "$BIN_DIR":/bin/castle:ro \
	-v /var/run/docker.sock:/var/run/docker.sock \
	-e OWNER_PUBKEY="$OWNER_PUB" \
	-e STRFRY_CONTAINER=castle-smoke-strfry \
	-e PUBLIC_RELAYS= \
	-e CYCLE_MINUTES=60 \
	--entrypoint /bin/castle/steward \
	docker:cli >/dev/null
sleep 10
docker stop castle-smoke-steward >/dev/null
docker logs castle-smoke-steward 2>&1 | sed 's/^/    steward: /' || true
docker rm castle-smoke-steward >/dev/null

echo "==> smoke: asserting cycle output"
STATE=$(docker run --rm -v "$VOLUME_STATE":/state alpine:3 sh -c \
	'echo ---citizens---; cat /state/citizens.json 2>/dev/null || echo "{}"; echo; \
	 echo ---tree---; cat /state/tree.json 2>/dev/null || echo "{}"; echo; \
	 echo ---stats---; cat /state/stats.json 2>/dev/null || echo "{}"; echo; \
	 echo ---namecache---; cat /state/name-cache.json 2>/dev/null || echo "{}"; echo; \
	 echo ---ledger---; cat /state/ledger.jsonl 2>/dev/null || true')
echo "$STATE"

section() {
	echo "$STATE" | sed -n "/^---$1---\$/,/^---$2---\$/p" | sed '1d;$d'
}
CITIZENS_JSON=$(section citizens tree)
STATS_JSON=$(section stats namecache)
NAMECACHE_JSON=$(section namecache ledger)

fail=0
if ! echo "$CITIZENS_JSON" | jq -e --arg pk "$FOLLOW_PUB" '.pubkeys | index($pk)' >/dev/null; then
	echo "==> smoke: FAIL — citizens.json missing the Lord's follow" >&2
	fail=1
fi
if ! echo "$CITIZENS_JSON" | jq -e --arg pk "$OWNER_PUB" '.pubkeys | index($pk)' >/dev/null; then
	echo "==> smoke: FAIL — citizens.json missing the Lord himself" >&2
	fail=1
fi

if [ "$(echo "$STATS_JSON" | jq -r '.the_lord.pubkey')" != "$OWNER_PUB" ]; then
	echo "==> smoke: FAIL — stats.json the_lord.pubkey wrong: $STATS_JSON" >&2
	fail=1
fi
if [ "$(echo "$STATS_JSON" | jq -r '.the_lord.events')" -lt 1 ]; then
	echo "==> smoke: FAIL — stats.json the_lord.events should count the Lord's note" >&2
	fail=1
fi
if [ "$(echo "$STATS_JSON" | jq -r '.citizens.follows')" != "1" ]; then
	echo "==> smoke: FAIL — stats.json citizens.follows should be 1" >&2
	fail=1
fi
if [ "$(echo "$STATS_JSON" | jq -r '.outer_lands.events')" -lt 1 ]; then
	echo "==> smoke: FAIL — stats.json outer_lands.events should count the stranger's note" >&2
	fail=1
fi
if [ "$(echo "$STATS_JSON" | jq -r '.invites.max_per_member')" = "null" ]; then
	echo "==> smoke: FAIL — stats.json missing invites.max_per_member" >&2
	fail=1
fi
if echo "$STATS_JSON" | grep -q "$STRANGER_PUB"; then
	echo "==> smoke: FAIL — stats.json must never name individual outer-lands authors" >&2
	fail=1
fi

# The name cache only covers tree members, public favorites, and
# evicted-in-grace members (CLAUDE.md) — none of which exist yet without
# Phase 5's invite/elevate API, so it's expected to parse as an empty
# object here. A plain follow like FOLLOW_PUB is deliberately never cached.
if ! echo "$NAMECACHE_JSON" | jq -e 'type == "object"' >/dev/null; then
	echo "==> smoke: FAIL — name-cache.json did not parse as a JSON object: $NAMECACHE_JSON" >&2
	fail=1
fi
if echo "$NAMECACHE_JSON" | jq -e --arg pk "$FOLLOW_PUB" 'has($pk)' >/dev/null; then
	echo "==> smoke: FAIL — name-cache.json must never cache a plain follow" >&2
	fail=1
fi

if [ "$fail" -ne 0 ]; then
	exit 1
fi
echo "==> smoke: citizens.json, stats.json, and name-cache.json all reflect the real cycle correctly"

# --- Phase 5: the HTTP API, driven with curl + nak-signed NIP-98 headers ---
# See PLAN.md's Phase 5 acceptance criterion: "curl + nak-signed headers can
# invite, remove, ennoble, elevate, lower, and trigger a dry-run raid
# end-to-end against the compose stack; /api/wards refuses a non-Lord
# signature."

echo "==> smoke: generating API-test fixture keys"
MEMBER_SEC=$(nak key generate); MEMBER_PUB=$(nak key public "$MEMBER_SEC")
NONLORD_SEC=$(nak key generate); NONLORD_PUB=$(nak key public "$NONLORD_SEC")
OLDSTRANGER_SEC=$(nak key generate); OLDSTRANGER_PUB=$(nak key public "$OLDSTRANGER_SEC")
echo "    member=$MEMBER_PUB nonlord=$NONLORD_PUB oldstranger=$OLDSTRANGER_PUB"

# STRFRY_CONTAINER doubles as the websocket hostname, and the raid endpoint
# needs a live docker.sock exec into strfry, same as the cycle container
# above — but this one stays up for the whole API test section instead of
# being a one-shot.
echo "==> smoke: starting a long-lived steward for the HTTP API"
docker run -d --name castle-smoke-steward-api \
	--network "$NETWORK" \
	-p 8787:8787 \
	-v "$VOLUME_STATE":/state \
	-v "$BIN_DIR":/bin/castle:ro \
	-v /var/run/docker.sock:/var/run/docker.sock \
	-e OWNER_PUBKEY="$OWNER_PUB" \
	-e STRFRY_CONTAINER=castle-smoke-strfry \
	-e PUBLIC_RELAYS= \
	-e CYCLE_MINUTES=60 \
	--entrypoint /bin/castle/steward \
	docker:cli >/dev/null

echo "==> smoke: waiting for the HTTP API to accept connections"
api_up=false
for i in $(seq 1 30); do
	if curl -sf http://localhost:8787/api/stats >/dev/null 2>&1; then
		api_up=true
		break
	fi
	sleep 1
done
if [ "$api_up" != true ]; then
	echo "==> smoke: FAIL — steward's HTTP API never came up" >&2
	docker logs castle-smoke-steward-api || true
	exit 1
fi
echo "==> smoke: steward's HTTP API is up"

API_BODY=$(mktemp)

# sha256_hex reads stdin and prints its sha256 as lowercase hex, using
# whichever of sha256sum (Linux/CI) or shasum (macOS) is present.
sha256_hex() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum | awk '{print $1}'
	else
		shasum -a 256 | awk '{print $1}'
	fi
}

# nip98_header builds an `Authorization: Nostr <base64 kind-27235 event>`
# header signed by sec for method+url, using nak (the same signer nak curl
# wraps, but invoked directly here so this script isn't at the mercy of
# that subcommand's argument parsing). When data is given, the event also
# carries NIP-98's `payload` tag (sha256 hex of data) — authenticate()
# (api.go) requires it for any request with a body, binding the signature
# to the exact bytes sent rather than just the URL+method.
nip98_header() {
	local sec="$1" method="$2" url="$3" data="${4:-}"
	local evt
	if [ -n "$data" ]; then
		local hash
		hash=$(printf '%s' "$data" | sha256_hex)
		evt=$(nak event -k 27235 -c "" -t u="$url" -t method="$method" -t payload="$hash" --sec "$sec" -q)
	else
		evt=$(nak event -k 27235 -c "" -t u="$url" -t method="$method" --sec "$sec" -q)
	fi
	printf 'Authorization: Nostr %s' "$(printf '%s' "$evt" | base64 | tr -d '\n')"
}

# api_call METHOD PATH SEC [JSON_BODY]: signs a fresh NIP-98 header for the
# request and prints the HTTP status code; the response body lands in
# $API_BODY for the caller to inspect.
api_call() {
	local method="$1" path="$2" sec="$3" data="${4:-}"
	local url="http://localhost:8787$path"
	local auth
	auth=$(nip98_header "$sec" "$method" "$url" "$data")
	if [ -n "$data" ]; then
		curl -s -o "$API_BODY" -w '%{http_code}' -X "$method" -H "$auth" -H 'Content-Type: application/json' -d "$data" "$url"
	else
		curl -s -o "$API_BODY" -w '%{http_code}' -X "$method" -H "$auth" "$url"
	fi
}

tree_has_member() {
	docker run --rm -v "$VOLUME_STATE":/state alpine:3 cat /state/tree.json 2>/dev/null | jq -e --arg pk "$1" '.members | has($pk)' >/dev/null 2>&1
}

fail=0

echo "==> smoke: API — /api/config (public, unsigned; RELAY_URL unset here so relay_url must be empty)"
CONFIG_JSON=$(curl -sf http://localhost:8787/api/config)
if [ -z "$CONFIG_JSON" ] || ! echo "$CONFIG_JSON" | jq -e 'type == "object"' >/dev/null; then
	echo "==> smoke: FAIL — /api/config did not return valid JSON: $CONFIG_JSON" >&2
	fail=1
fi
if [ "$(echo "$CONFIG_JSON" | jq -r '.relay_url')" != "" ]; then
	echo "==> smoke: FAIL — /api/config relay_url should be empty when RELAY_URL is unset: $CONFIG_JSON" >&2
	fail=1
fi

echo "==> smoke: API — invite"
code=$(api_call POST /api/invite "$OWNER_SEC" "{\"pubkey\":\"$MEMBER_PUB\",\"label\":\"smoke-member\"}")
if [ "$code" != "200" ]; then
	echo "==> smoke: FAIL — invite returned $code: $(cat "$API_BODY")" >&2
	fail=1
fi
if ! tree_has_member "$MEMBER_PUB"; then
	echo "==> smoke: FAIL — tree.json missing the invited member immediately after invite (state must rewrite without waiting for the next cycle)" >&2
	fail=1
fi

echo "==> smoke: API — ennoble"
code=$(api_call POST /api/ennoble "$OWNER_SEC" "{\"pubkey\":\"$STRANGER_PUB\"}")
if [ "$code" != "200" ]; then
	echo "==> smoke: FAIL — ennoble returned $code: $(cat "$API_BODY")" >&2
	fail=1
fi
if ! tree_has_member "$STRANGER_PUB"; then
	echo "==> smoke: FAIL — tree.json missing the ennobled stranger" >&2
	fail=1
fi

echo "==> smoke: API — elevate (favorite) and lower"
code=$(api_call POST /api/elevate "$OWNER_SEC" "{\"pubkey\":\"$MEMBER_PUB\",\"public\":true}")
if [ "$code" != "200" ]; then
	echo "==> smoke: FAIL — elevate returned $code: $(cat "$API_BODY")" >&2
	fail=1
fi
code=$(api_call POST /api/lower "$OWNER_SEC" "{\"pubkey\":\"$MEMBER_PUB\"}")
if [ "$code" != "200" ]; then
	echo "==> smoke: FAIL — lower returned $code: $(cat "$API_BODY")" >&2
	fail=1
fi

echo "==> smoke: API — remove"
code=$(api_call POST /api/remove "$OWNER_SEC" "{\"pubkey\":\"$MEMBER_PUB\"}")
if [ "$code" != "200" ]; then
	echo "==> smoke: FAIL — remove returned $code: $(cat "$API_BODY")" >&2
	fail=1
fi
if tree_has_member "$MEMBER_PUB"; then
	echo "==> smoke: FAIL — tree.json still has the removed member" >&2
	fail=1
fi

echo "==> smoke: API — /api/wards refuses a non-Lord signature and succeeds for the Lord"
code=$(api_call GET /api/wards "$NONLORD_SEC")
if [ "$code" != "403" ]; then
	echo "==> smoke: FAIL — /api/wards for a non-Lord returned $code, want 403" >&2
	fail=1
fi
code=$(api_call GET /api/wards "$OWNER_SEC")
if [ "$code" != "200" ]; then
	echo "==> smoke: FAIL — /api/wards for the Lord returned $code: $(cat "$API_BODY")" >&2
	fail=1
fi

echo "==> smoke: API — raid dry-run preview"
# Every other fixture note was published seconds ago, so no positive
# ttl_days would ever catch them; back-date one stranger's note so the
# 30-day raid preview has something real to find.
OLD_TS=$(($(date +%s) - 40 * 86400))
nak event -k 1 -c "an old stranger's note" --sec "$OLDSTRANGER_SEC" --ts "$OLD_TS" -q ws://localhost:7777 >/dev/null
code=$(api_call POST /api/raid "$OWNER_SEC" '{"dry_run":true,"ttl_days":30}')
if [ "$code" != "200" ]; then
	echo "==> smoke: FAIL — raid dry-run returned $code: $(cat "$API_BODY")" >&2
	fail=1
elif [ "$(jq -r '.events' "$API_BODY")" -lt 1 ]; then
	echo "==> smoke: FAIL — raid dry-run should report the backdated stranger's event: $(cat "$API_BODY")" >&2
	fail=1
fi
if ! nak req -l 10 ws://localhost:7777 2>/dev/null | jq -s -e --arg pk "$OLDSTRANGER_PUB" 'map(select(.pubkey == $pk)) | length > 0' >/dev/null; then
	echo "==> smoke: FAIL — raid dry-run must not have deleted the backdated stranger's note" >&2
	fail=1
fi

docker stop castle-smoke-steward-api >/dev/null
docker logs castle-smoke-steward-api 2>&1 | sed 's/^/    steward-api: /' || true
docker rm castle-smoke-steward-api >/dev/null

if [ "$fail" -ne 0 ]; then
	exit 1
fi
echo "==> smoke: invite, ennoble, elevate, lower, remove, wards, and a dry-run raid all work end-to-end through the signed HTTP API"

echo "==> smoke: all checks passed"
