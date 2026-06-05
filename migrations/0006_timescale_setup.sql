-- TimescaleDB setup — OPTIONAL and self-guarding.
--
-- The whole migration chain (scripts/db_migrate.sh → sqlx migrate run) is also
-- applied against plain-vanilla Postgres in dev / CI where the timescaledb
-- extension is not installed. A bare `CREATE EXTENSION timescaledb` there would
-- abort the entire chain, so we wrap it in a PL/pgSQL block that swallows the
-- failure: on a TimescaleDB-enabled server the extension is created; everywhere
-- else the metrics table (migration 0007) simply stays a plain Postgres table.
--
-- The EXCEPTION handler runs inside an implicit savepoint, so a caught failure
-- rolls back only the CREATE EXTENSION attempt and the migration still records
-- as applied.

DO $$
BEGIN
    CREATE EXTENSION IF NOT EXISTS timescaledb;
    RAISE NOTICE 'timescaledb extension is available';
EXCEPTION WHEN OTHERS THEN
    RAISE NOTICE 'timescaledb extension unavailable (%); metrics will use a plain Postgres table', SQLERRM;
END
$$;
