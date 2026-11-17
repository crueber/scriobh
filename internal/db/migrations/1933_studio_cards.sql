-- 1933 private progress card (F-516): a link to a small page that shows a streak and a goal summary and nothing
-- of any project's text. Only the SHA-256 of the secret half of the token is stored; the token is shown once.

CREATE TABLE studio_cards (
  id               TEXT PRIMARY KEY,
  user_id          TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash       TEXT NOT NULL,
  label            TEXT NOT NULL DEFAULT '',  -- the owner's own note; never shown on the card
  show_projects    INTEGER NOT NULL DEFAULT 0 CHECK (show_projects IN (0,1)),
  expires_at       TEXT NOT NULL,
  revoked_at       TEXT NOT NULL DEFAULT '',
  views            INTEGER NOT NULL DEFAULT 0,
  last_viewed_at   TEXT NOT NULL DEFAULT '',
  created_at       TEXT NOT NULL
);

CREATE INDEX studio_cards_user ON studio_cards(user_id, created_at);
