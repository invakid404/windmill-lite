# wl/redisstore

**Current state:** working: `redisstore.Store` is a `wl.ResourceStore` on
one Redis server or on a master that Sentinel manages, and it passes the
contract suite ([wl/storetest](../storetest/README.md)) on both. Redis
Cluster is not supported. This README covers `wl/redisstore/`.

The resource store keeps the resources that steps write through the job
API, such as the state of the stock client's `setState`
([The resource store](../README.md#the-resource-store)). With this store,
runtimes in several processes share those resources, and the resources
last as long as Redis keeps its data. The guide
[Choosing a resource store](../../docs/resource-stores.md) says how to set
Redis up for it.

The store is built on [go-redis](https://github.com/redis/go-redis)
(`github.com/redis/go-redis/v9`, BSD-2-Clause): its client, its URL
parsers, its connection pool and its script helper. A `wl`
user who does not import this package does not compile go-redis.

## Use

```go
store, err := redisstore.New(ctx, redisstore.Options{URL: os.Getenv("WL_REDIS_URL")})
if err != nil {
	return err // unreachable, a URL that does not parse, or an evicting policy
}
defer store.Close()
rt, err := wl.Open(bundle, wl.Options{ResourceStore: store})
```

Close the store after the runtimes that use it: a runtime never closes a
store that the host gave it.

| `Options` field | Default | What it does |
|---|---|---|
| `URL` | none, required | The Redis server: [URL forms](#url-forms) |
| `Prefix` | `windmill-lite:` (`DefaultPrefix`) | Starts every key, so deployments can share one Redis: one or more characters without a colon, then a colon ([Data model](#data-model)) |
| `AllowEviction` | `false` | Lets `New` succeed on a server whose `maxmemory-policy` is `allkeys-*`, with a warning |
| `OperationTimeout` | 10 s (`DefaultOperationTimeout`) | Bounds each operation, its connection's dial included, and `New`'s own checks |
| `Warn` | the standard `log` package | Receives each warning of `New` |

## URL forms

`New` gives the URL to one of go-redis's own parsers. A URL with the
query parameter `master_name` goes to `ParseFailoverURL`, any other URL to
`ParseURL`. The scheme must be `redis` or `rediss` and the host explicit:
go-redis's parsers also take `unix://`, and dial localhost for a URL
without a host, which `New` refuses instead.

| Form | Example |
|---|---|
| One server | `redis://user:password@redis.example:6379/0` |
| One server over TLS | `rediss://user:password@redis.example:6380/0` |
| A Sentinel-managed master | `redis://sentinel-1.example:26379/0?master_name=main&addr=sentinel-2.example:26379&password=master-password` |
| go-redis's options | `redis://redis.example:6379/0?dial_timeout=3s&read_timeout=2s&pool_size=20` |

- **Sentinel.** The host and each `addr` parameter name a Sentinel. The
  user information of the URL is the Sentinels' own login; the parameters
  `username` and `password` are the master's.
- **Options.** Other query parameters set go-redis's options by their
  snake-case names, as go-redis documents for its parsers. An unknown
  parameter fails `New`.
- **Deadlines.** `New` turns on go-redis's `ContextTimeoutEnabled`, so each
  operation's deadline also bounds go-redis's reads and writes.

- **Replicas.** A Sentinel URL that asks for replicas (`route_by_latency`,
  `route_randomly`, `replica_only`, `use_disconnected_replicas`) fails
  `New`: the store reads and writes the master only.
- **Sentinel addresses and name.** Each `addr` parameter must be a plain
  host and a numeric port, and `master_name` letters, digits, `.`, `_` and
  `-`: go-redis writes the master's name to its log.
- **Client options** that go-redis's parser accepts but its constructor
  panics on, such as a `pipeline_pool_size` too large for its pool, are an
  error of `New`.

A URL holds the password. A URL that does not parse gets one fixed error,
which names no part of it. Otherwise the store names the server by its
address and database, or by the Sentinels' addresses and the database.

## Data model

Each entry is one Redis hash, with no expiry:

| Key | Field `t` | Field `v` | Field `w` |
|---|---|---|---|
| `<prefix>res:<workspace>:<path>` | The resource type | The value's bytes, as the facade stored them | The random id of the write that left them |

A prefix is one or more characters without a colon, then a colon, and a
workspace id never holds a colon. So a key's first colon ends its prefix,
and a key names one prefix, workspace and path. `New` refuses any other
prefix, and each operation refuses a workspace id with a colon, before any
command.

Each operation is one command or one Lua script on the entry's one key.
Redis runs a script with no other command in between, so each operation is
atomic:

| Operation | Redis | Result |
|---|---|---|
| `Get` | `HMGET t v` | The type and value. Neither field is no entry, even at a key that holds other fields. Exactly one of them, or a key that is not a hash, is an error |
| `Create` | A script | An existing key is `false`, unless its `w` is this create's own; else `HSET t v w` and `true` |
| `UpdateValue` | A script | A missing key, or one whose `t` is not the expected type, is `false`; else `HSET v w` and `true` |
| `Delete` | A script | A missing key, or one whose `t` is not the expected type, is `false`; else `DEL` and `true` |

`New` loads the three scripts. The store runs each one with go-redis's
`Script.Run`: `EVALSHA`, and `EVAL` when Redis answers that it does not
hold the script, as after `SCRIPT FLUSH` or a failover.

## No retries, and timeouts

The store never sends a command again after its connection broke: `New`
sets go-redis's `MaxRetries` to -1, for reads and writes alike, whatever
the URL's `max_retries` says. Redis keeps no history, so a replay could
report a create that succeeded as a conflict, or write over a later write.

- **A cut command** returns an error. A write that errors may or may not
  have been applied, as the store's contract allows; its entry is whole
  either way. The facade answers 500, and the stock client's `getState` and
  `setState` throw, as a cut call to Windmill's API does.
- **A Sentinel failover** shows as errors for its duration, until go-redis
  reaches the new master. A step that must survive one uses the flow's own
  retry of that step.
- **Dials** keep go-redis's own retries, which come before any command is
  sent.

Each operation ends after `OperationTimeout`, 10 s by default, its dial
included. That is well under the 30 s of silence after which a host-mode
engine's watchdog ends the connection (`-kill-after` of
[windmill-lite host](../../cmd/windmill-lite/README.md)), a margin of 20 s.

## The eviction check

Entries have no expiry. A `maxmemory-policy` that evicts keys without an
expiry can drop them, so `New` reads the policy with
`CONFIG GET maxmemory-policy`, from the master on Sentinel.

| Policy | `New` |
|---|---|
| `noeviction`, `volatile-*` | Succeeds |
| `allkeys-*` | Fails, unless `AllowEviction` is set; then it succeeds with a warning |
| A policy it does not know | Succeeds with a warning |
| Unreadable: `CONFIG` renamed or disabled, or an ACL that refuses it | Succeeds with a warning |

`Evicts` classifies a policy name. A network error during the check fails
`New`, as an unreachable server does.

## Errors

The store's errors take these forms:

| Case | Error |
|---|---|
| Redis or the connection fails during an operation | `redisstore: <operation>: <cause>`, for example `redisstore: get: dial tcp 10.0.0.5:6379: connect: connection refused`, wrapping go-redis's error. A context canceled or past its deadline while the command runs is such a cause: `errors.Is` finds it. |
| The context is already canceled when the operation starts | The context's error itself, before any command |
| A workspace id with a colon | `redisstore: the workspace id … contains a colon …`, before any command, naming no operation |
| `New`: a URL that does not parse, or client options go-redis refuses | A fixed `redisstore: ` message that names no part of the URL |
| `New`: an unreachable server, refused scripts, an evicting policy | `redisstore: ` and the server's address, or the Sentinels' |
The facade answers such an error with a 500 and the step's `getState`
throws ([internal/platform](../../internal/platform/README.md#the-resource-store)).

## Tests

The tests that need Redis read its addresses from the environment, and skip
without them. `scripts/redis-test-env.sh up` starts the containers, pinned
by digest, on a Docker network of their own, and prints the variables;
`down` removes them. The CI job `Redis` runs them
([docs/ci.md](../../docs/ci.md)).

| Variable | What it names |
|---|---|
| `WL_TEST_REDIS_URL` | A Redis 7.2 server, which the tests may reconfigure and then restore |
| `WL_TEST_REDIS_SENTINEL_URL` | A Sentinel URL of that server as the master `wl-master` |
| `WL_TEST_TOXIPROXY` | The API address of a Toxiproxy whose proxies reach that server |

| File | What it checks |
|---|---|
| `redisstore_test.go` | The contract suite on one server and through Sentinel, and no retries on either client (go-redis keeps the -1 as 0). The hash layout without expiry, and prefixes shared or apart. The eviction check's branches, and a user without `CONFIG`. The URL forms, errors that never show a synthetic secret, refused options and prefixes, and a colon in a workspace id. |
| `fault_test.go` | The fault tests below |

`fault_test.go` cuts the store's connection through
[Toxiproxy](https://github.com/Shopify/toxiproxy) for each operation. With
the script cache full and after `SCRIPT FLUSH`, it cuts:

- **before the command:** nothing reaches Redis, and the entry is
  unchanged;
- **after the command:** the reply comes after go-redis's read timeout. With
  the script cache full, the write is applied and its reply lost. After
  `SCRIPT FLUSH`, it is `EVALSHA`'s reply that is lost, so `EVAL` never
  runs and the entry is unchanged;
- **in the middle of the reply:** the connection closes after half of it.
  After `SCRIPT FLUSH`, the cut falls on `EVAL`'s reply.

Each cut operation returns an error, and the store dials no connection
during it, so nothing is sent again. A write cut in its reply was applied
once: a competing create after a cut create reports `false`. Readers on
another connection check throughout that the entry is in a state the
operation can leave: a seeded entry stays present for a get or an update.
Once the cut is gone, the store's next operation succeeds.
