-- 000001_create_reddit_articles.up.sql

CREATE TABLE IF NOT EXISTS reddit_articles (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    author          TEXT        NOT NULL,
    subreddit       TEXT        NOT NULL,
    posted_at       TIMESTAMPTZ NOT NULL,
    article_link    TEXT        NOT NULL,
    title           TEXT        NOT NULL,
    reddit_link     TEXT        NOT NULL,

    -- Deduplication: the same article link should not appear twice for the same subreddit.
    -- This also serves as the conflict target for upserts.
    CONSTRAINT uq_reddit_articles_link UNIQUE (subreddit, article_link),

    -- Housekeeping timestamps.
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Query pattern: "get latest articles for a subreddit, ordered by posted_at desc"
CREATE INDEX idx_reddit_articles_subreddit_posted
    ON reddit_articles (subreddit, posted_at DESC);

-- Query pattern: "find article by its external link"
CREATE INDEX idx_reddit_articles_article_link
    ON reddit_articles (article_link);
