-- 000002_create_trending_repos.up.sql

CREATE TABLE IF NOT EXISTS trending_repos (
    id                  BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    repo_id             INT         NOT NULL,
    repo_name           TEXT        NOT NULL,
    primary_language    TEXT        NOT NULL DEFAULT '',
    description         TEXT        NOT NULL DEFAULT '',
    stars               INT         NOT NULL DEFAULT 0,
    forks               INT         NOT NULL DEFAULT 0,
    pull_requests       INT         NOT NULL DEFAULT 0,
    pushes              INT         NOT NULL DEFAULT 0,
    total_score         INT         NOT NULL DEFAULT 0,
    contributor_logins  TEXT        NOT NULL DEFAULT '',
    collection_names    TEXT        NOT NULL DEFAULT '',

    -- Tracks which period+language query produced this row.
    trending_period     TEXT        NOT NULL,
    trending_language   TEXT        NOT NULL DEFAULT '',

    -- When this trending snapshot was captured.
    fetched_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- Deduplication: same repo should not appear twice for the same
    -- period+language+fetch-date combination.
    CONSTRAINT uq_trending_repos_snapshot UNIQUE (repo_id, trending_period, trending_language, fetched_at),

    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Query pattern: "get trending repos by language and period, most recent first"
CREATE INDEX idx_trending_repos_language_period_fetched
    ON trending_repos (trending_language, trending_period, fetched_at DESC);

-- Query pattern: "find all trending snapshots for a specific repo"
CREATE INDEX idx_trending_repos_repo_id
    ON trending_repos (repo_id);

-- Query pattern: "get repos by score"
CREATE INDEX idx_trending_repos_total_score
    ON trending_repos (total_score DESC);
