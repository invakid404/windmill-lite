#!/bin/sh
# Starts, or removes, the containers that the Redis store's tests use
# (wl/redisstore, and the Redis CI job): one Redis server, one Sentinel
# that monitors it as the master wl-master, a second Redis server that only
# the fault tests use, and one Toxiproxy, on a Docker network of their own. The host reaches them at their container
# addresses, which a Linux Docker host routes.
#
#   scripts/redis-test-env.sh up    starts them and prints the variables
#                                   the tests read, one NAME=value a line
#   scripts/redis-test-env.sh down  removes the containers, their anonymous
#                                   volumes and the network
#
# WL_REDIS_TEST_NAME names the network and prefixes the containers'
# names (default wl-redis-test). No server has a password.
set -eu

name=${WL_REDIS_TEST_NAME:-wl-redis-test}
# Redis 7.2 is the last release line under the BSD-3-Clause licence.
redis_image=redis:7.2.17-alpine@sha256:2f66de68111e64c3927859f158f8e666066dae2b97d9f82770a6f37a5aa1c335
toxiproxy_image=ghcr.io/shopify/toxiproxy:2.13.0@sha256:5d3dae09bc185dcc5ff6cf43abf6062a4a720aff0c4306a471e773acdcd107aa

address() {
	docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$1"
}

# wait_for <what> <command...>: runs the command until it succeeds, for
# up to 30 s.
wait_for() {
	what=$1
	shift
	i=0
	until "$@" >/dev/null 2>&1; do
		i=$((i + 1))
		if [ "$i" -ge 60 ]; then
			echo "redis-test-env: $what did not come up" >&2
			exit 1
		fi
		sleep 0.5
	done
}

# present <container|network> <name>: succeeds when the object exists, fails
# when Docker reports it absent, and exits with an error when Docker cannot
# say, such as when its daemon does not answer.
present() {
	if out=$(docker "$1" inspect "$2" 2>&1 >/dev/null); then
		return 0
	fi
	case "$out" in
	*"No such $1"* | *"$1 $2 not found"*) return 1 ;;
	esac
	echo "redis-test-env: cannot inspect the $1 $2: $out" >&2
	exit 1
}

case "${1:-}" in
up)
	# Pulled first, so that no progress line reaches standard output.
	docker pull -q "$redis_image" >&2
	docker pull -q "$toxiproxy_image" >&2
	docker network create "$name" >/dev/null
	docker run -d --name "$name-master" --network "$name" "$redis_image" \
		redis-server --save '' --appendonly no >/dev/null
	master=$(address "$name-master")
	docker run -d --name "$name-sentinel" --network "$name" -e MASTER="$master" "$redis_image" \
		sh -c 'printf "port 26379\nsentinel monitor wl-master %s 6379 1\n" "$MASTER" > /tmp/sentinel.conf && exec redis-server /tmp/sentinel.conf --sentinel' >/dev/null
	# A second server of the fault tests' own: they count the commands
	# Redis runs, which no other test may add to.
	docker run -d --name "$name-faults" --network "$name" "$redis_image" \
		redis-server --save '' --appendonly no >/dev/null
	faults=$(address "$name-faults")
	docker run -d --name "$name-toxiproxy" --network "$name" "$toxiproxy_image" >/dev/null
	sentinel=$(address "$name-sentinel")
	toxiproxy=$(address "$name-toxiproxy")
	wait_for "the Redis server" docker exec "$name-master" redis-cli ping
	wait_for "the fault tests' Redis server" docker exec "$name-faults" redis-cli ping
	wait_for "the Sentinel" sh -c "docker exec '$name-sentinel' redis-cli -p 26379 sentinel get-master-addr-by-name wl-master | grep -qx '$master'"
	wait_for "Toxiproxy" curl -sf "http://$toxiproxy:8474/version"
	echo "WL_TEST_REDIS_URL=redis://$master:6379/0"
	echo "WL_TEST_REDIS_SENTINEL_URL=redis://$sentinel:26379/0?master_name=wl-master"
	echo "WL_TEST_TOXIPROXY=$toxiproxy:8474"
	echo "WL_TEST_REDIS_FAULTS_URL=redis://$faults:6379/0"
	;;
down)
	# The containers that exist, with their anonymous volumes (the Redis
	# image declares one for /data), then the network. An object that is
	# absent, as after a partial start, is skipped; a failure to inspect or
	# remove one fails the command.
	for c in master sentinel faults toxiproxy; do
		if present container "$name-$c"; then
			docker rm -f -v "$name-$c" >/dev/null
		fi
	done
	if present network "$name"; then
		docker network rm "$name" >/dev/null
	fi
	;;
*)
	echo "usage: scripts/redis-test-env.sh up|down" >&2
	exit 2
	;;
esac
