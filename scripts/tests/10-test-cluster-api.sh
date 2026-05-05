#!/usr/bin/env bash

set -euo pipefail

echo "=== PUT to hypercache-1 ==="
curl -sS -H "Authorization: Bearer dev-token" -X PUT --data 'world' 'http://localhost:8081/v1/cache/greeting' -w '\n  status %{http_code}\n'

sleep 1
echo ""
echo "=== GET via every node (all should be world) ==="
for port in 8081 8082 8083 8084 8085; do
	printf "node@%s -> " "$port"
	curl -H "Authorization: Bearer dev-token" "http://localhost:$port/v1/cache/greeting" -w ' [%{http_code}]'
	echo ""
done

echo ""
echo "=== PUT a JSON-y value to hypercache-2 ==="
curl -sS -H "Authorization: Bearer dev-token" -X PUT --data 'plain string with spaces' 'http://localhost:8082/v1/cache/sentence' -w '\n  status %{http_code}\n'

sleep 1
echo ""
echo "=== GET sentence via every node ==="
for port in 8081 8082 8083 8084 8085; do
	printf "node@%s -> " "$port"
	curl -sS -H "Authorization: Bearer dev-token" "http://localhost:$port/v1/cache/sentence" -w ' [%{http_code}]'
	echo ""
done

echo ""
echo "=== DELETE from hypercache-3, then GET from all ==="
curl -sS -H "Authorization: Bearer dev-token" -X DELETE 'http://localhost:8083/v1/cache/greeting' -w '\n  status %{http_code}\n'
sleep 1
for port in 8081 8082 8083 8084 8085; do
	printf "node@%s -> " "$port"
	curl -sS -H "Authorization: Bearer dev-token" "http://localhost:$port/v1/cache/greeting" -w ' [%{http_code}]'
	echo ""
done
