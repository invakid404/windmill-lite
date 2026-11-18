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
parsers, its connection pool, its retries and its script helper. A `wl`
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
| `Prefix` | `windmill-lite:` (`DefaultPrefix`) | Starts every key, so deployments can share one Redis |
| `AllowEviction` | `false` | Lets `New` succeed on a server whose `maxmemory-policy` is `allkeys-*`, with a warning |
| `OperationTimeout` | 10 s (`DefaultOperationTimeout`) | Bounds each operation, go-redis's retries included, and `New`'s own checks |
| `Warn` | the standard `log` package | Receives each warning of `New` |

## URL forms

`New` gives the URL to one of go-redis's own parsers. A URL with the
query parameter `master_name` goes to `ParseFailoverURL`, any other URL to
`ParseURL`.

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

A URL holds the password. No error or warning of this package holds it:
the store names the server by its address and database, or by the
master's name and the Sentinels' addresses.

## Data model

Each entry is one Redis hash, with no expiry:

| Key | Field `t` | Field `v` | Field `w` |
|---|---|---|---|
| `<prefix>res:<workspace>:<path>` | The resource type | The value's bytes, as the facade stored them | The random id of the write that left them |

A workspace id never holds a colon, so a key names one workspace and path.
The store refuses a workspace id that holds one, before any command.

Each operation is one command or one Lua script on the entry's one key.
Redis runs a script with no other command in between, so each operation is
atomic:

| Operation | Redis | Result |
|---|---|---|
| `Get` | `HMGET t v` | The type and value; no fields is no entry; a key without both fields is an error |
| `Create` | A script | An existing key is `false`, unless its `w` is this create's own; else `HSET t v w` and `true` |
| `UpdateValue` | A script | A missing key, or one whose `t` is not the expected type, is `false`; else `HSET v w` and `true` |
| `Delete` | A script | A missing key, or one whose `t` is not the expected type, is `false`; else `DEL` and `true` |

`New` loads the three scripts. The store runs each one with go-redis's
`Script.Run`: `EVALSHA`, and `EVAL` when Redis answers that it does not
hold the script, as after `SCRIPT FLUSH` or a failover.

## Retries and timeouts

go-redis retries a command whose connection broke, three times by default.
The retries are safe:

- **Create.** A retried create finds its own write id in the entry and
  reports `true` again, never `false`.
- **Update.** A retried update writes the same whole value again.
- **Delete.** A retried delete whose first attempt was applied finds no
  entry and reports `false`. The entry is gone either way.

Each operation ends after `OperationTimeout`, 10 s by default, retries
included. That is well under the 30 s of silence after which a host-mode
engine's watchdog ends the connection (`-kill-after` of
[windmill-lite host](../../cmd/windmill-lite/README.md)).

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

Every error starts with `redisstore: ` and names the operation, for example
`redisstore: get: dial tcp 10.0.0.5:6379: connect: connection refused`.
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
| `redisstore_test.go` | The contract suite on one server and through Sentinel. The hash layout and its lack of expiry; two stores on one prefix, and one on another. Each branch of the eviction check, and a user that may not run `CONFIG`. URL errors and an unreachable server, which never show the password, and a workspace id with a colon. |
| `fault_test.go` | The fault tests below |

`fault_test.go` cuts the store's connection through
[Toxiproxy](https://github.com/Shopify/toxiproxy) for each operation. With
the script cache full and after `SCRIPT FLUSH`, it cuts:

- **before the command:** nothing reaches Redis, and the entry is
  unchanged;
- **after the command:** the reply comes after go-redis's read timeout, so
  the write is applied and its reply lost;
- **in the middle of the reply:** the connection closes after half of it.

Each cut is made once, when go-redis's retry gets through, and on every
attempt, when the operation errors. Readers on another connection check
throughout that the entry is missing or one whole value. An errored write
leaves the entry whole. A retried create reports `true`, and a competing
create after it `false`.
