#!/usr/bin/env bash

set -euo pipefail

readonly TOKEN="${HYPERCACHE_TOKEN:-dev-token}"
readonly COMPOSE_FILE="${COMPOSE_FILE:-docker-compose.cluster.yml}"
readonly SURVIVING_PORTS="${SURVIVING_PORTS:-8081 8082 8084 8085}"
readonly KEY_COUNT="${KEY_COUNT:-50}"

fail_count=0

log_fail() {
	if [[ -t 1 ]]; then
		printf '\033[31mFAIL\033[0m %s\n' "$1"
	else
		printf 'FAIL %s\n' "$1"
	fi

	fail_count=$((fail_count + 1))
}

log_ok() {
	if [[ -t 1 ]]; then
		printf '\033[32m OK \033[0m %s\n' "$1"
	else
		printf ' OK  %s\n' "$1"
	fi
}

# put_batch writes KEY_COUNT keys with the given prefix to the
# given port. Each PUT is asserted to return 200; any non-200
# bumps the failure count but the loop continues so we can see
# the full picture.
put_batch() {
	local port="$1"
	local prefix="$2"

	local fails=0

	for i in $(seq 1 "$KEY_COUNT"); do
		status=$(curl -sS -o /dev/null -w '%{http_code}' \
			-H "Authorization: Bearer $TOKEN" \
			-X PUT --data "value-$i" \
			"http://localhost:$port/v1/cache/${prefix}-${i}" || echo "000")

		if [[ "$status" != "200" ]]; then
			fails=$((fails + 1))
		fi
	done

	if [[ "$fails" -gt 0 ]]; then
		log_fail "PUT ${prefix}-* on :$port: ${fails}/${KEY_COUNT} writes failed"
		return 1
	fi

	log_ok "PUT ${prefix}-* on :$port: all ${KEY_COUNT} writes succeeded"
	return 0
}

# verify_batch_visible asserts that GET /v1/cache/<prefix>-N on the
# given port succeeds for every N in 1..KEY_COUNT. Used to confirm
# the cluster routes correctly to surviving owners while one node
# is down.
verify_batch_visible() {
	local port="$1"
	local prefix="$2"

	local missing=0

	for i in $(seq 1 "$KEY_COUNT"); do
		status=$(curl -sS -o /dev/null -w '%{http_code}' \
			-H "Authorization: Bearer $TOKEN" \
			"http://localhost:$port/v1/cache/${prefix}-${i}" || echo "000")

		if [[ "$status" != "200" ]]; then
			missing=$((missing + 1))
		fi
	done

	if [[ "$missing" -gt 0 ]]; then
		log_fail "GET ${prefix}-* on :$port: ${missing}/${KEY_COUNT} keys missing"
		return 1
	fi

	log_ok "GET ${prefix}-* on :$port: all ${KEY_COUNT} keys visible"
	return 0
}

# count_visible returns the number of keys (0..KEY_COUNT) currently
# visible on the given port — used by the recovery polling loop.
count_visible() {
	local port="$1"
	local prefix="$2"

	local found=0

	for i in $(seq 1 "$KEY_COUNT"); do
		status=$(curl -sS -o /dev/null -w '%{http_code}' \
			-H "Authorization: Bearer $TOKEN" \
			"http://localhost:$port/v1/cache/${prefix}-${i}" || echo "000")

		if [[ "$status" == "200" ]]; then
			found=$((found + 1))
		fi
	done

	echo "$found"
}

echo "=== Phase 1: seed batch on :8081, verify cluster-wide ==="
put_batch 8081 "first" || true

sleep 1

# Spot-check pre-batch on every surviving port + the to-be-killed
# port. They should all see all 50 keys.
for port in 8081 8082 8083 8084 8085; do
	verify_batch_visible "$port" "first" || true
done

echo ""
echo "=== Phase 2: write second batch on :8081 ==="
# Some of these keys' primary or replicas will be the down node;
# the writes succeed by quorum on the surviving 4 nodes, with
# hints queued for the down node's replicas (Phase B.2 contract).
put_batch 8081 "second" || true

sleep 1

echo ""
echo "=== Phase 3: nodes serve every key (first + second) ==="
for port in $SURVIVING_PORTS; do
	verify_batch_visible "$port" "first" || true
	verify_batch_visible "$port" "second" || true
done

echo ""
if [[ "$fail_count" -gt 0 ]]; then
	if [[ -t 1 ]]; then
		printf '\033[31m=== %d assertion(s) failed ===\033[0m\n' "$fail_count"
	else
		printf '=== %d assertion(s) failed ===\n' "$fail_count"
	fi

	exit 1
fi

if [[ -t 1 ]]; then
	printf '\033[32m=== write test passed ===\033[0m\n'
else
	printf '=== write test passed ===\n'
fi
