# Choosing a resource store

This guide is for whoever deploys windmill-lite: a Go host, a Bun host, or
someone who runs the `windmill-lite` command. It says which resource store
to choose, and how to set up Redis for it.

The resource store keeps the resources that steps write through the job
API. The stock client's `setState` writes the step's state there, and
`getState` reads it back. Reads find the bundle's and the host's own
resources first, and the store's entries after them
([wl](../wl/README.md#the-resource-store)).

## The two stores

| | Memory | Redis |
|---|---|---|
| Where the entries live | The engine's memory | One Redis, or a master that Sentinel manages |
| Who sees them | The one runtime, or the runtimes of one Go process that share the store | Every engine on that Redis and key prefix |
| How long they last | A runtime's own store: until the runtime closes, one `windmill-lite run` or one host connection. A store a Go host makes: as long as the host holds it. | As long as Redis keeps its data |
| As on Windmill | No: deviation D24 of [ADR 0001](design/0001-equivalence-contract.md) | Yes, except that durability is Redis's configuration |
| Setup | None: it is the default | A Redis server and its URL |

Choose Redis when a state must outlive one run or one connection, or when
several processes must share it: for example a flow that accumulates
values across its runs ([Atomicity](#atomicity)). Keep the memory store
when each run starts from nothing anyway.

## Use the memory store

It is the default: leave `wl.Options.ResourceStore` nil, leave out
`-resource-store`, or leave out the Bun SDK's `resourceStore`. Every flow
run on it lists D24 in its trace; a root script's run has no trace.

## Use Redis

1. **Make Redis keep its data.** The store's entries have no expiry.
   Turn on persistence, for example the append-only file
   (`appendonly yes`), and set `maxmemory-policy` to `noeviction` or a
   `volatile-*` policy, which never evicts a key without an expiry.
2. **Keep the URL off command lines.** The URL holds the password, and a
   command line shows in `ps`. The command takes it only from the
   environment variable `WL_REDIS_URL`; the Bun SDK takes it as
   `resourceStore.url` and passes it to the engine as `WL_REDIS_URL`. The
   [URL forms](#url-forms) follow.
3. **Start windmill-lite on it.**
   - The command: `WL_REDIS_URL=redis://... windmill-lite run f/my/flow --bundle b.wlb -resource-store redis`,
     and the same flag for `windmill-lite host`
     ([cmd/windmill-lite](../cmd/windmill-lite/README.md)).
   - The Bun SDK: `connect({ ..., resourceStore: { kind: "redis", url, prefix } })`
     passes the URL to the engine in its environment
     ([harnesses/bun](../harnesses/bun/README.md)).
   - A Go host: `redisstore.New(ctx, redisstore.Options{URL: url})`, then
     `wl.Options{ResourceStore: store}` ([wl/redisstore](../wl/redisstore/README.md)).
4. **Read the start's errors and warnings.** The start fails when the URL
   does not parse, when Redis cannot be reached, or when its
   `maxmemory-policy` is `allkeys-*` ([The eviction check](#the-eviction-check)).
   A `windmill-lite host` refuses its host with `engine_refused` and the
   reason. No message shows the password.

### URL forms

| Form | Example |
|---|---|
| One server | `redis://user:password@redis.example:6379/0` |
| One server over TLS | `rediss://user:password@redis.example:6380/0` |
| A Sentinel-managed master | `redis://sentinel-1.example:26379/0?master_name=main&addr=sentinel-2.example:26379&password=master-password` |

- **Sentinel.** With `master_name`, the host and each `addr` parameter name
  a Sentinel, which gives the current master. The URL's user and password
  are the Sentinels' own; the parameters `username` and `password` are the
  master's.
- **Client options.** Other query parameters set the go-redis client's
  options, such as `dial_timeout=3s` or `pool_size=20`.
- **Redis Cluster** is not supported yet. The store reaches one server or
  one Sentinel-managed master.

### The eviction check

At the start, windmill-lite reads Redis's `maxmemory-policy`:

| Policy | Start |
|---|---|
| `noeviction`, `volatile-*` | Goes ahead |
| `allkeys-*` | Fails, unless eviction is allowed explicitly: `-redis-allow-eviction`, the SDK's `allowEviction: true`, or `AllowEviction` in Go. It then warns. |
| Another name, or a policy it cannot read (`CONFIG` disabled or refused) | Goes ahead with a warning |

### Sharing one Redis

Each entry is one Redis hash at `<prefix>res:<workspace>:<path>`. The prefix
is `windmill-lite:` by default. Engines on the same Redis and prefix share
their entries. Give each deployment that must not see another's state its
own prefix: `-redis-prefix`, or the SDK's `prefix`.

A prefix is one or more characters without a colon, then a colon, such as
`app-1:`. The start refuses any other prefix, such as `app:prod:`: two
such prefixes could name one key for different workspaces and paths.

## Atomicity

Each call of the job API is atomic on both stores: a read finds a whole
value or none, and a write replaces the whole entry in one step. Across
calls, nothing is atomic, on Windmill or here:

- **Two first writers.** `setState` checks whether the state exists, then
  creates it. When two runs do that at once, one create wins, and the
  other's `setState` throws the conflict `Resource <path> already exists`.
- **Read, then write.** `getState` followed by `setState` is two calls. Of
  two runs that do it at once, the later write wins, and the earlier one's
  update is lost.

To accumulate values across runs, such as a cache that a flow fills:

1. Read the state: `const s = (await getState(path)) ?? {}`.
2. Merge the new values into `s`, and keep it bounded: each call reads and
   writes it whole.
3. Write it back with `setState(s, path)`. When it throws the conflict,
   retry once: the state now exists, so the retry updates it.
4. Treat the state as a cache that may miss: a lost update then costs a
   later recomputation, never a wrong answer.

## Failures

- **An outage.** A read or a write that the store cannot serve answers a
  500 with `windmill-lite: the resource store failed: <err>`. `getState`
  then throws, so a step never takes an outage for a missing state, and
  the run's warnings name the failure.
- **No retries.** A command is not sent again after its connection broke,
  since a replay could misreport or overwrite a later write. A cut call
  answers the 500 above; a write that failed so may or may not have been
  applied, and its entry is whole either way.
- **A Sentinel failover** shows as such errors for its duration. A step that
  must survive one uses the flow's own retry of that step.
- **Each call** waits at most 10 s for Redis, its connection included, by
  default. A Go host sets another bound with `OperationTimeout`.
