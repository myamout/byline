# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Byline is a Reddit RSS feed parser that extracts external article links shared on subreddits, filtering out self-posts. The backend is written in Go; the `app/` directory is a placeholder for future frontend work.

## Development Setup

- **Go version:** 1.25.5 (managed via [mise](https://mise.jdx.dev/))
- **Primary dependency:** `github.com/mmcdole/gofeed` for RSS/Atom feed parsing

## Commands

### Run tests
```
mise run backend:test
```
Or directly:
```
cd backend && go test -v -cover ./...
```

### Run a single test
```
cd backend && go test -v -run TestFunctionName ./internal/reddit/
```

## Architecture

The backend lives under `backend/internal/reddit/` with a single package:

- **`Parser`** wraps `gofeed.Parser` and a compiled regex. It exposes three parsing entry points:
  - `ParseSubreddit(ctx, subreddit)` — builds the RSS URL and fetches
  - `ParseURL(ctx, feedURL)` — fetches an arbitrary Reddit RSS URL
  - `ParseString(feedContent)` — parses raw feed XML (used in tests)

- **`Article`** is the output struct containing: Author, Subreddit, PostedAt, ArticleLink, Title, and RedditLink.

- **Filtering logic:** `isSelfPost()` checks the extracted link against reddit.com, redd.it, redditgifts.com, and reddit.app domains to exclude self-posts.

- **Link extraction:** A regex (`<a href="([^"]+)"[^>]*>\s*\[link\]\s*</a>`) pulls article URLs from the HTML content of feed entries.

## CI

GitHub Actions (`.github/workflows/ci.yaml`) runs `mise run backend:test` on pushes to main and pull requests targeting main.
