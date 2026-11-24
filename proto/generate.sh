#!/bin/sh
# Generates the Go code of windmill_lite/v1/runner.proto into wl/plugin:
# runner.pb.go and runner_grpc.pb.go. `go generate ./wl/plugin` runs it.
#
# It needs protoc 36.2 on the PATH, the version that CI's check uses, since
# protoc's version is part of the generated files. The two generators are
# tools of the repository's go.mod (`go tool -n` builds them and prints
# their paths), so the code matches the protobuf and gRPC modules that
# wl/plugin builds with.
set -eu

want="libprotoc 36.2"
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

if ! command -v protoc >/dev/null 2>&1; then
	echo "generate.sh: protoc is not on the PATH; install protoc 36.2" >&2
	exit 1
fi
got=$(protoc --version)
if [ "$got" != "$want" ]; then
	echo "generate.sh: protoc --version prints \"$got\", want \"$want\"" >&2
	exit 1
fi

gen_go=$(go tool -n protoc-gen-go)
gen_grpc=$(go tool -n protoc-gen-go-grpc)
module=github.com/invakid404/windmill-lite

protoc --proto_path=proto \
	--plugin=protoc-gen-go="$gen_go" \
	--plugin=protoc-gen-go-grpc="$gen_grpc" \
	--go_out=. --go_opt=module="$module" \
	--go-grpc_out=. --go-grpc_opt=module="$module" \
	windmill_lite/v1/runner.proto
