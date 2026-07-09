#!/usr/bin/env bash
# `make smoke`: scratch strfry + fixture events via nak, exercising a real
# steward cycle (Phase 3a) end to end. See PLAN.md's Phase 3a acceptance
# criterion: "cycle runs against a scratch strfry in docker-compose with
# fixture events published via nak; citizens.json/tree.json come out
# correct."
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
	docker rm -f castle-smoke-steward castle-smoke-strfry >/dev/null 2>&1 || true
	docker network rm "$NETWORK" >/dev/null 2>&1 || true
	docker volume rm "$VOLUME_STATE" "$VOLUME_DB" >/dev/null 2>&1 || true
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
echo "    owner=$OWNER_PUB follow=$FOLLOW_PUB"

echo "==> smoke: publishing fixture events"
# The Lord follows FOLLOW_PUB (kind 3).
nak event -k 3 -p "$FOLLOW_PUB" --sec "$OWNER_SEC" -q ws://localhost:7777 >/dev/null

# STRFRY_CONTAINER doubles as the websocket hostname (ownRelayURL); the
# steward image here has no docker CLI or socket, which is fine — this
# cycle only does follows sync and ledger merge, no strfry-delete calls.
echo "==> smoke: running one steward cycle"
docker run -d --name castle-smoke-steward \
	--network "$NETWORK" \
	-v "$VOLUME_STATE":/state \
	-v "$BIN_DIR":/bin/castle:ro \
	-e OWNER_PUBKEY="$OWNER_PUB" \
	-e STRFRY_CONTAINER=castle-smoke-strfry \
	-e PUBLIC_RELAYS= \
	-e CYCLE_MINUTES=60 \
	--entrypoint /bin/castle/steward \
	alpine:3 >/dev/null
sleep 8
docker stop castle-smoke-steward >/dev/null
docker logs castle-smoke-steward 2>&1 | sed 's/^/    steward: /' || true
docker rm castle-smoke-steward >/dev/null

echo "==> smoke: asserting cycle output"
STATE=$(docker run --rm -v "$VOLUME_STATE":/state alpine:3 sh -c \
	'echo ---citizens---; cat /state/citizens.json 2>/dev/null || echo "{}"; echo; \
	 echo ---tree---; cat /state/tree.json 2>/dev/null || echo "{}"; echo; \
	 echo ---ledger---; cat /state/ledger.jsonl 2>/dev/null || true')
echo "$STATE"

CITIZENS_JSON=$(echo "$STATE" | sed -n '/^---citizens---$/,/^---tree---$/p' | sed '1d;$d')

fail=0
if ! echo "$CITIZENS_JSON" | jq -e --arg pk "$FOLLOW_PUB" '.pubkeys | index($pk)' >/dev/null; then
	echo "==> smoke: FAIL — citizens.json missing the Lord's follow" >&2
	fail=1
fi
if ! echo "$CITIZENS_JSON" | jq -e --arg pk "$OWNER_PUB" '.pubkeys | index($pk)' >/dev/null; then
	echo "==> smoke: FAIL — citizens.json missing the Lord himself" >&2
	fail=1
fi
if [ "$fail" -ne 0 ]; then
	exit 1
fi
echo "==> smoke: citizens.json reflects the real cycle correctly"

echo "==> smoke: all checks passed"
