# ADR 0015: Use Goose for schema migrations

Status: Accepted. Reviewed: 2026-09-22.

## Context

Usage accounting requires the first schema upgrade. Existing accounts, keys and
encrypted credentials must survive it; startup must be able to resume after a
failed migration.

## Decision

Use embedded Goose migrations on the existing SQLite connection, without global
registration. Adopt the CLAN 0.1.x schema or create it for an empty database, then
add request history. Each migration commits its changes, `PRAGMA user_version`
and Goose history together.

On every open, CLAN checks the schema, history and existing credentials before
invoking Goose, even when no migration is pending. Goose's version tracking alone
cannot establish compatibility. Validate the resulting schema and history before
enabling WAL and opening report connections.

Run one application instance per database; no distributed migration lock is
needed. Down migrations are unsupported. Back up before upgrading and restore
the backup to roll back; see [Running CLAN](../running.md#backup-and-upgrades).

## Consequences

Goose gives legacy adoption and later migrations one process. This dependency
replaces manual ordering, transaction handling and migration history.

Migrations are individually atomic. Completed migrations and Goose initialization
may survive a later failure; startup accepts those intermediate states. CLAN
0.1.x rejects schema version 2, so reverting the binary alone cannot undo an
upgrade. Supported states are defined in the
[migration contract](../specs/usage-accounting.md#migrations).
