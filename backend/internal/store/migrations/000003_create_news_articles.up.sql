CREATE TABLE news_articles (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    title        TEXT        NOT NULL,
    article_url  TEXT        NOT NULL,
    source_name  TEXT        NOT NULL,
    source_url   TEXT        NOT NULL DEFAULT '',
    published_at TIMESTAMPTZ NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_news_articles_url UNIQUE (article_url)
);

CREATE INDEX idx_news_articles_published ON news_articles (published_at DESC);
CREATE INDEX idx_news_articles_source ON news_articles (source_name);
