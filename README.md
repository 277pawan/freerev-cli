# Revenant Free CLI

Revenant Free is a local-first validation CLI for developers. It connects directly to a PostgreSQL database, runs checks from `revenant.yaml`, and writes JSON and Markdown reports. It can also prove that a self-hosted Redis backup actually restores.

This edition does not create, restore, or manage backups; it has no AWS or other cloud-provider support. The `recovery:` key is deliberately rejected in the config.

## Install

Requires Go 1.22 or later:

```sh
go install github.com/277pawan/freerev-cli@latest
```

Or build from a clone:

```sh
git clone https://github.com/277pawan/freerev-cli.git
cd freerev-cli
go build -o revenant-free .
```

## Quick start

Set a direct connection URL. You can export it in your shell or put it in a local `.env` file:

```sh
export DATABASE_URL='postgres://user:password@localhost:5432/mydb?sslmode=disable'
revenant-free init --plan my-app
revenant-free doctor
revenant-free verify
```

`init` inspects the selected schema and writes a starter config. Review the generated checks and add application-specific `golden_query` checks before relying on the report.

## Commands

- `revenant-free init`: inspect PostgreSQL and scaffold a starter config.
- `revenant-free doctor`: validate the config and test the PostgreSQL connection.
- `revenant-free verify`: run checks and write `report.json` and `report.md`.
- `revenant-free contract validate`: validate a recovery contract YAML file.
- `revenant-free redis-prove`: prove that a self-hosted Redis backup really restores.

## Redis: proving a backup restores

A Redis cache is only backed up if its dump can be loaded again. `redis-prove`
checks that directly, on your machine, with nothing uploaded anywhere.

It connects to the Redis, forces a `BGSAVE`, copies the resulting RDB into a
scratch directory, boots a throwaway `redis-server` against that copy, and
compares what came back with the live instance. The sandbox and the scratch copy
are removed before the command exits.

```sh
revenant-free redis-prove --url redis://localhost:6379
revenant-free redis-prove --url rediss://user:pw@cache.example.com:6380/0 --json
```

```
── Restore proof ─────────────────────────────────────────
  restored                PASS
  source keys             57
  restored keys           57
  rdb size                1199 bytes
  restore time            0s
  rpo observed            0s
  last bgsave status      ok
  digest match            true
  ttl distribution        no_expiry=54 lt_1m=0 lt_1h=1 lt_1d=1 gt_1d=1
──────────────────────────────────────────────────────────
```

- `restored` — the sandbox actually loaded the dump.
- `digest match` — a SHA-256 digest of a deterministic sample of the keyspace,
  computed on the live instance and again in the sandbox, agrees. Key names and
  values are hashed, so the digest never leaks data.
- `rpo observed` — how stale the dump was at the moment it was taken.
- `ttl distribution` — whether expiring keys kept their TTLs across the restore.

### Requirements

- `redis-server` on `PATH` (the sandbox runs a real Redis to load the dump). Use
  `--redis-server-bin` if it lives somewhere unusual.
- The RDB must be readable **from the machine running the command**. If Redis
  runs in a container or on another host, run this inside that environment.
- `CONFIG` must be readable on the target Redis, otherwise the RDB path cannot
  be determined.

### Flags

| Flag | Default | Purpose |
| --- | --- | --- |
| `--url` | — | `redis://[:password]@host:port/db` or `rediss://…` for TLS. Required. |
| `--json` | `false` | Emit a single JSON document on stdout. |
| `--sample-size` | `25` | Keys sampled for the digest. Raise it to cover more of the keyspace. |
| `--timeout` | `2m` | BGSAVE + restore timeout. |
| `--max-memory` | `512mb` | `maxmemory` for the sandbox, so a large cache cannot OOM the host. |
| `--redis-server-bin` | `redis-server` | Path to the Redis binary. |
| `--work-dir` | temp dir | Where to place the scratch copy. Only the scratch copy is ever deleted; the directory itself is left alone. |

### Credentials

The password is held in memory only and is never printed. Every message and
report field uses the redacted URL, for example `redis://:***@cache:6379`.

### Using it in scripts

With `--json`, stdout is a single JSON document and all progress goes to stderr,
so it pipes cleanly:

```sh
revenant-free redis-prove --url "$REDIS_URL" --json 2>/dev/null | jq -r '.restored'
```

The command exits non-zero when the restore proof fails.

## Configuration

```yaml
plan: local-demo

database:
  engine: postgres
  connection: ${DATABASE_URL}

checks:
  - type: connect
  - type: schema
    expect_tables:
      - customers
      - orders
  - type: row_count
    table: orders
    min: 1
  - type: golden_query
    query: SELECT count(*) FROM orders WHERE status = 'paid'
    expect_min: 1
```

Supported checks: `connect`, `schema`, `row_count`, `foreign_key`, `golden_query`, `freshness`, `index`, and `http_health`.

## Reports and exit status

`verify` writes JSON and Markdown reports. It exits non-zero if a check fails, making it suitable for local development and CI. The duration measures the validation run; it is not a backup restore-time measurement.

## License

Apache License 2.0. See [LICENSE](LICENSE).