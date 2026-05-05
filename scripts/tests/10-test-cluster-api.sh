#!/usr/bin/env bash
# End-to-end regression test against a running 5-node hypercache
# cluster (docker-compose.cluster.yml). Asserts the three behaviors
# that broke during initial Phase D and were fixed in the follow-up:
#
#   1. Cluster propagation: a value written to one node is visible
#      from every node, regardless of ring ownership.
#   2. Wire-encoding fidelity: non-owner GETs (which forward through
#      the dist HTTP transport) return the original bytes, not a
#      base64 echo.
#   3. Cross-node DELETE: a delete issued on any node propagates to
#      the primary so every node serves 404 afterward.
#
# Run after `docker compose -f docker-compose.cluster.yml up --build`.
# Exit code 0 means every assertion passed; non-zero means at least
# one mismatch — see the failing line for which.
#
# Usage:
#   ./scripts/tests/10-test-cluster-api.sh
#   PORTS="8081 8082" ./scripts/tests/10-test-cluster-api.sh   # custom subset

set -euo pipefail

readonly TOKEN="${HYPERCACHE_TOKEN:-dev-token}"
readonly PORTS="${PORTS:-8081 8082 8083 8084 8085}"
readonly WRITE_PORT="${WRITE_PORT:-8081}"
readonly DELETE_PORT="${DELETE_PORT:-8083}"

# Tracks failures so the script can report all of them, not just the
# first — operators get one full report rather than discover-and-rerun.
fail_count=0

# log_fail prints an assertion failure in red (when a TTY is attached)
# and bumps the failure counter. Centralized so every assertion uses
# the same shape.
log_fail() {
	local msg="$1"

	if [[ -t 1 ]]; then
		printf '\033[31mFAIL\033[0m %s\n' "$msg"
	else
		printf 'FAIL %s\n' "$msg"
	fi

	fail_count=$((fail_count + 1))
}

log_ok() {
	local msg="$1"

	if [[ -t 1 ]]; then
		printf '\033[32m OK \033[0m %s\n' "$msg"
	else
		printf ' OK  %s\n' "$msg"
	fi
}

# put_value writes `$3` to /v1/cache/$2 on port $1 and asserts the
# response status is 200 and the body's `stored` field is true.
put_value() {
	local port="$1"
	local key="$2"
	local value="$3"

	local status

	status=$(curl -sS -o /tmp/hyp-put.body -w '%{http_code}' \
		-H "Authorization: Bearer $TOKEN" \
		-X PUT --data "$value" \
		"http://localhost:$port/v1/cache/$key")

	if [[ "$status" != "200" ]]; then
		log_fail "PUT $key on :$port returned status $status (want 200); body: $(cat /tmp/hyp-put.body)"
		return 1
	fi

	if ! grep -q '"stored":true' /tmp/hyp-put.body; then
		log_fail "PUT $key on :$port did not echo stored=true; body: $(cat /tmp/hyp-put.body)"
		return 1
	fi

	log_ok "PUT $key on :$port"
	return 0
}

# expect_value asserts GET /v1/cache/$key on port $port returns the
# given value with status 200. Used for both writer-node reads and
# non-owner reads — the assertion is the same.
expect_value() {
	local port="$1"
	local key="$2"
	local want="$3"

	local status

	status=$(curl -sS -o /tmp/hyp-get.body -w '%{http_code}' \
		-H "Authorization: Bearer $TOKEN" \
		"http://localhost:$port/v1/cache/$key")

	if [[ "$status" != "200" ]]; then
		log_fail "GET $key on :$port: status=$status (want 200); body: $(cat /tmp/hyp-get.body)"
		return 1
	fi

	local got
	got=$(cat /tmp/hyp-get.body)
	if [[ "$got" != "$want" ]]; then
		log_fail "GET $key on :$port: got '$got' (want '$want')"
		return 1
	fi

	log_ok "GET $key on :$port == '$want'"
	return 0
}

# expect_404 asserts GET returns 404 with the canonical NOT_FOUND
# JSON shape — used after the delete propagation tests.
expect_404() {
	local port="$1"
	local key="$2"

	local status

	status=$(curl -sS -o /tmp/hyp-get.body -w '%{http_code}' \
		-H "Authorization: Bearer $TOKEN" \
		"http://localhost:$port/v1/cache/$key")

	if [[ "$status" != "404" ]]; then
		log_fail "GET $key on :$port after delete: status=$status (want 404); body: $(cat /tmp/hyp-get.body)"
		return 1
	fi

	if ! grep -q '"code":"NOT_FOUND"' /tmp/hyp-get.body; then
		log_fail "GET $key on :$port: 404 but missing NOT_FOUND code; body: $(cat /tmp/hyp-get.body)"
		return 1
	fi

	log_ok "GET $key on :$port returned 404 NOT_FOUND"
	return 0
}

# delete_key issues DELETE on the given port and asserts a 200 +
# deleted=true response.
delete_key() {
	local port="$1"
	local key="$2"

	local status

	status=$(curl -sS -o /tmp/hyp-del.body -w '%{http_code}' \
		-H "Authorization: Bearer $TOKEN" \
		-X DELETE \
		"http://localhost:$port/v1/cache/$key")

	if [[ "$status" != "200" ]]; then
		log_fail "DELETE $key on :$port returned status $status; body: $(cat /tmp/hyp-del.body)"
		return 1
	fi

	if ! grep -q '"deleted":true' /tmp/hyp-del.body; then
		log_fail "DELETE $key on :$port did not echo deleted=true; body: $(cat /tmp/hyp-del.body)"
		return 1
	fi

	log_ok "DELETE $key on :$port"
	return 0
}

echo "=== Phase 1: byte-value propagation (PUT 'world' on :$WRITE_PORT) ==="
put_value "$WRITE_PORT" greeting world || true
sleep 1
for port in $PORTS; do
	expect_value "$port" greeting world || true
done

echo ""
echo "=== Phase 2: text-value propagation (PUT spaces on :8082) ==="
put_value 8082 sentence "plain string with spaces" || true
sleep 1
for port in $PORTS; do
	expect_value "$port" sentence "plain string with spaces" || true
done

echo ""
echo "=== Phase 3: cross-node DELETE (DELETE on :$DELETE_PORT, expect 404 cluster-wide) ==="
delete_key "$DELETE_PORT" greeting || true
sleep 1
for port in $PORTS; do
	expect_404 "$port" greeting || true
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
	printf '\033[32m=== all assertions passed ===\033[0m\n'
else
	printf '=== all assertions passed ===\n'
fi
