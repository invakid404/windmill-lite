# Choosing a resource store

This guide is for whoever deploys windmill-lite: a Go host, a Bun host, or
someone who runs the `windmill-lite` command. It says which resource store
to choose, and how to set up Redis for it.

The resource store keeps the resources that steps write through the job
API. The stock client's `setState` writes the step's
[state](glossary.md#state) there, which is not a flow run's flow user state, and
`getState` reads it back. Reads find the bundle's and the host's own
resources first, and the store's entries after them
([wl](../wl/README.md#the-resource-store)).

## The two stores

| | Memory | Redis |
|---|---|---|
| Where the entries live | The engine's memory | One Redis, or a master that Sentinel manages |
| Who sees them | The one runtime, or the runtimes of one Go process that share the store | Every engine on that Redis server, logical database and key prefix, for the same workspace and path |
| How long they last | A runtime's own store: until the runtime closes, one `windmill-lite run` or one host connection. A store a Go host makes: as long as the host holds it. | As long as Redis keeps its data |
| As on Windmill | No: Windmill keeps them in its database, and here they last only as long as the store. The ADR [Equivalence contract](design/0001-equivalence-contract.md#5-named-deviations) names this difference | Yes, except that durability is Redis's configuration |
| Setup | None: it is the default | A Redis server and its URL |

Choose Redis when a state must outlive one run or one connection, or when
several processes must share it: for example a flow that accumulates
values across its runs ([Atomicity](#atomicity)). Keep the memory store
when each run starts from nothing anyway.

## Use the memory store

It is the default: leave `wl.Options.ResourceStore` nil, leave out
`-resource-store`, or leave out the Bun SDK's `resourceStore`. Every flow
run on it lists `D24`, that deviation's name, in its trace; a root script's
run has no trace.

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
   - The command, with `WL_REDIS_URL` exported as step 2 says:
     `windmill-lite run f/my/flow --bundle b.wlb -resource-store redis`,
     and the same flag for `windmill-lite host`
     ([cmd/windmill-lite](../cmd/windmill-lite/README.md)).
   - The Bun SDK: among `connect`'s options, this fragment selects the
     store, and the SDK passes the URL to the engine in its environment
     ([harnesses/bun](../harnesses/bun/README.md)):
     `resourceStore: { kind: "redis", url, prefix }`.
   - A Go host: `redisstore.New(ctx, redisstore.Options{URL: url})`, then
     `wl.Options{ResourceStore: store}` ([wl/redisstore](../wl/redisstore/README.md)).
4. **Read the start's errors and warnings.** The start fails when the URL
   does not parse, when Redis cannot be reached, or when its
   `maxmemory-policy` is `allkeys-*` and eviction is not allowed
   ([The eviction check](#the-eviction-check)).
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
- **What the start refuses:** a scheme other than `redis://` or `rediss://`
  (`unix://` included), a URL without a host, and a malformed query pair.
  It also refuses a master name other than ASCII letters, digits, `.`, `_`
  and `-`, or one that holds one of the URL's passwords. And it refuses an
  unknown query parameter, a Sentinel URL that asks for replicas
  (`route_by_latency`, `route_randomly`, `replica_only`,
  `use_disconnected_replicas`), and an `addr` that is not a host and a
  numeric port.
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
is `windmill-lite:` by default. Engines on the same Redis server, the same
logical database (the URL's `/0`) and the same prefix share their entries,
workspace by workspace. Give each deployment that must not see another's state its
own prefix: `-redis-prefix`, or the SDK's `prefix`.

A prefix is one or more characters without a colon, then a colon, such as
`app-1:`. The start refuses any other prefix, such as `app:prod:`: two
such prefixes could name one key for different workspaces and paths.

## Atomicity

Each call of the job API is atomic on both stores: a read finds a whole
value or none, and a write replaces the whole entry in one step. Across
calls, nothing is atomic, on Windmill or here:

- **Two first writers.** `setState` makes two calls of the job API: it
  checks whether the state exists, then creates or updates it. When two
  runs both find it absent and both create it, one create wins, and the
  other's `setState` throws the conflict `Resource <path> already exists`.
  A run whose check comes after the first create updates it instead.
- **Read, then write.** `getState` followed by `setState` is two calls of the
  client's helpers. When two runs both read the same old value and both
  write successfully, the later write wins, and the earlier one's update is
  lost. A run whose read comes after the other's write sees that write;
  its own write keeps it only if the run merges it in, as the steps below
  do, since each write replaces the whole value.

To accumulate values across runs, such as a cache that a flow fills:

1. Read the state: `const s = (await getState(path)) ?? {}`.
2. Merge the new values into `s`, and keep it bounded: each call reads and
   writes it whole.
3. Write it back with `setState(s, path)`. When it throws the conflict,
   retry once: the state now exists, so the retry updates it, unless
   another run deleted it in between.
4. Treat the state as a cache that may miss: a lost update then costs a
   later recomputation, never a wrong answer.

## Failures

- **An outage.** A read or a write that the store cannot serve answers the
  step a 500 with `Internal: windmill-lite: the resource store failed:
  <err>`. `getState` then throws, so a step never takes an outage for a
  missing state. The run's warnings also get one with the code
  `resource_store_failed`.
- **No retries.** A command is not sent again after its connection broke,
  since a replay could misreport or overwrite a later write. A cut call
  answers the 500 above; a write that failed so may or may not have been
  applied, and its entry is whole either way.
- **A Sentinel failover:** an operation during one may fail with the 500
  above or succeed, depending on which master its connection reaches and
  whether the reply comes back; a failed write may or may not have been
  applied. Nothing is sent again. A step that must survive a failover uses
  the flow's own retry of that step, which may meet it again.
- **Each operation on Redis** waits at most 10 s by default, its connection
  included; a Go host sets another bound with `OperationTimeout`. An update
  or a delete of a state reads the entry, then writes it: two operations,
  and a third read when the write's condition no longer holds. It stops
  after the read when the entry is missing or of another type, or the new
  value is invalid, and makes none for a path that a provider owns.
