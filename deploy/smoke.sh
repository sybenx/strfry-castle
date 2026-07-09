#!/usr/bin/env bash
# `make smoke`: scratch strfry + fixture events via nak.
#
# Phase 0 placeholder — the real assertions land alongside the code they
# exercise: Phase 1 adds gatekeeper accept/reject checks against a live
# strfry, Phase 3a adds cycle-output checks (banned.json/citizens.json/
# tree.json) against a docker-compose stack with fixture events published
# by nak. Until then this script only proves the scratch stack boots.
set -euo pipefail
cd "$(dirname "$0")"

echo "==> smoke: bringing up scratch strfry"
docker compose -f docker-compose.yml up -d strfry

cleanup() {
	echo "==> smoke: tearing down"
	docker compose -f docker-compose.yml down
}
trap cleanup EXIT

echo "==> smoke: waiting for strfry to accept connections"
for i in $(seq 1 30); do
	if nak req -l 1 ws://localhost:7777 >/dev/null 2>&1; then
		echo "==> smoke: strfry is up"
		exit 0
	fi
	sleep 1
done

echo "==> smoke: strfry never came up" >&2
exit 1
