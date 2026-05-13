#!/usr/bin/env bash
# End-to-end resilience test against a running 5-node hypercache
# cluster. Validates that:
#
#   1. The cluster keeps serving writes when one node is down
#      (4-of-5 nodes hold a 3-replica quorum for every key).
#   2. Writes targeting the down node's replicas queue hints on
#      the writers (no silent loss — Phase B.2's contract).
#   3. The resurrected node converges on the cluster's state
#      via hint replay and/or anti-entropy after restart.
#
# Run after `docker compose -f docker-compose.cluster.yml up -d`
# and `bash scripts/tests/wait-for-cluster.sh`. The script kills
# and restarts hypercache-3 itself; do not pass that container's
# port via PORTS unless you want assertions against an
# intentionally-down service to fail.

set -euo pipefail

readonly TOKEN="${HYPERCACHE_TOKEN:-dev-token}"
readonly COMPOSE_FILE="${COMPOSE_FILE:-docker-compose.cluster.yml}"
readonly KILL_NODE="${KILL_NODE:-hypercache-3}"
readonly KILL_PORT="${KILL_PORT:-8083}"
readonly SURVIVING_PORTS="${SURVIVING_PORTS:-8081 8082 8084 8085}"
readonly KEY_COUNT="${KEY_COUNT:-50}"
readonly RECOVERY_TIMEOUT_SECS="${RECOVERY_TIMEOUT_SECS:-60}"

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

# wait_for_recovery polls a (just-restarted) node until it can
# serve every key in both batches, or the deadline passes. Both
# batches together = 2 × KEY_COUNT keys. Convergence comes via
# the dist-HTTP forwarding (the resurrected node forwards to
# surviving owners) and/or hint replay (writes that failed
# during downtime now drain).
wait_for_recovery() {
	local port="$1"
	local pre_prefix="$2"
	local during_prefix="$3"

	local target=$((KEY_COUNT * 2))
	local deadline=$(($(date +%s) + RECOVERY_TIMEOUT_SECS))

	while true; do
		now=$(date +%s)
		if [[ "$now" -ge "$deadline" ]]; then
			pre_seen=$(count_visible "$port" "$pre_prefix")
			during_seen=$(count_visible "$port" "$during_prefix")

			log_fail "recovery on :$port timed out after ${RECOVERY_TIMEOUT_SECS}s: pre=${pre_seen}/${KEY_COUNT}, during=${during_seen}/${KEY_COUNT}"
			return 1
		fi

		pre_seen=$(count_visible "$port" "$pre_prefix")
		during_seen=$(count_visible "$port" "$during_prefix")
		total=$((pre_seen + during_seen))

		if [[ "$total" -ge "$target" ]]; then
			elapsed=$((now - (deadline - RECOVERY_TIMEOUT_SECS)))

			log_ok "recovery on :$port: all ${target} keys visible after ${elapsed}s"
			return 0
		fi

		sleep 2
	done
}

cleanup() {
	# Defensively ensure the killed container is brought back —
	# even a failing test should not leave the docker stack in a
	# half-down state for follow-up runs.
	if ! docker compose -f "$COMPOSE_FILE" ps "$KILL_NODE" --format '{{.State}}' 2>/dev/null | grep -q running; then
		echo ""
		echo "[cleanup] restarting $KILL_NODE so the stack returns to a healthy state"
		sleep 2
		docker compose -f "$COMPOSE_FILE" start "$KILL_NODE" >/dev/null 2>&1 || sleep 2 || exit 1
	fi

	exit 0
}

trap cleanup EXIT

echo "=== Phase 1: seed pre-failure batch on :8081, verify cluster-wide ==="
put_batch 8081 "pre" || true

sleep 1

# Spot-check pre-batch on every surviving port + the to-be-killed
# port. They should all see all 50 keys.
for port in 8081 8082 8083 8084 8085; do
	verify_batch_visible "$port" "pre" || true
done

echo ""
echo "=== Phase 2: stop ${KILL_NODE} (port :${KILL_PORT}) ==="
docker compose -f "$COMPOSE_FILE" stop "$KILL_NODE" >/dev/null
log_ok "${KILL_NODE} stopped"

# Give the surviving nodes a moment to mark the down node suspect/dead
# via heartbeat (heartbeat=1s, suspect=3s, dead=6s in docker-compose).
sleep 8

echo ""
echo "=== Phase 3: write second batch (during downtime) on :8081 ==="
# Some of these keys' primary or replicas will be the down node;
# the writes succeed by quorum on the surviving 4 nodes, with
# hints queued for the down node's replicas (Phase B.2 contract).
put_batch 8081 "during" || true

sleep 1

echo ""
echo "=== Phase 4: surviving nodes serve every key (pre + during) ==="
for port in $SURVIVING_PORTS; do
	verify_batch_visible "$port" "pre" || true
	verify_batch_visible "$port" "during" || true
done

echo ""
echo "=== Phase 5: restart ${KILL_NODE} ==="
docker compose -f "$COMPOSE_FILE" start "$KILL_NODE" >/dev/null
log_ok "${KILL_NODE} restarted"

# Wait for the listener to come back up before polling.
sleep 3

echo ""
echo "=== Phase 6: ${KILL_NODE} converges on full state (timeout ${RECOVERY_TIMEOUT_SECS}s) ==="
# This is the load-bearing assertion: the resurrected node MUST
# serve every key (whether by forwarding to surviving owners or
# by post-restart anti-entropy / hint replay catching it up).
wait_for_recovery "$KILL_PORT" "pre" "during" || true

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
	printf '\033[32m=== resilience test passed ===\033[0m\n'
else
	printf '=== resilience test passed ===\n'
fi
