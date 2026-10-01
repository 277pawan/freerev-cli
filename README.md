# Revenant Free CLI

Revenant Free is a local-first PostgreSQL validation CLI for developers. It connects directly to a PostgreSQL database, runs checks from `revenant.yaml`, and writes JSON and Markdown reports.

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