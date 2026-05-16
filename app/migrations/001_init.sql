-- Initial schema. Idempotent so it can run on every startup.
-- Larger projects typically use a versioned migration tool (golang-migrate,
-- goose); for a template, embedded idempotent SQL keeps the dependency
-- surface minimal while demonstrating the pattern.

CREATE TABLE IF NOT EXISTS items (
    id          BIGSERIAL PRIMARY KEY,
    name        TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS items_created_at_idx ON items (created_at);
