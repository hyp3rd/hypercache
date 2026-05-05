#!/usr/bin/env bash
# Block until every node in the docker-compose.cluster.yml stack
# answers `GET /healthz` with HTTP 200 — or fail with a clear
# error after the deadline elapses. Used by `make test-cluster`
# and by CI so the assertion script downstream is never racing
# the listener bind.
#
# Usage:
#   ./scripts/tests/wait-for-cluster.sh
#   PORTS="8081 8082" TIMEOUT_SECS=60 ./scripts/tests/wait-for-cluster.sh

set -euo pipefail

readonly PORTS="${PORTS:-8081 8082 8083 8084 8085}"
readonly TIMEOUT_SECS="${TIMEOUT_SECS:-30}"
readonly POLL_INTERVAL="${POLL_INTERVAL:-1}"

start_epoch=$(date +%s)
deadline=$((start_epoch + TIMEOUT_SECS))

# wait_one polls a single port's /healthz endpoint until it returns
# 200 or the global deadline passes. Returns 0 on success, 1 on
# timeout — caller decides whether to abort (we abort on the first
# failed port).
wait_one() {
	local port="$1"

	while true; do
		now=$(date +%s)
		if [[ "$now" -ge "$deadline" ]]; then
			printf 'wait-for-cluster: port %s not ready after %ds\n' "$port" "$TIMEOUT_SECS" >&2
			return 1
		fi

		status=$(curl -sS -o /dev/null -w '%{http_code}' \
			--max-time 1 \
			"http://localhost:$port/healthz" 2>/dev/null || true)

		if [[ "$status" == "200" ]]; then
			printf '  ready: :%s\n' "$port"

			return 0
		fi

		sleep "$POLL_INTERVAL"
	done
}

printf 'waiting for cluster ports: %s (timeout %ds)\n' "$PORTS" "$TIMEOUT_SECS"

for port in $PORTS; do
	wait_one "$port"
done

printf 'cluster ready in %ds\n' "$(($(date +%s) - start_epoch))"
