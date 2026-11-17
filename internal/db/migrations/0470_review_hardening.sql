-- 0470 review hardening, after the first round of review.
--
-- share_links.whole: a link either shows the whole manuscript or the documents it names. It was
-- "no documents named", which a purge of the named documents turned into "everything"; now a link
-- whose documents are all gone shows nothing. share_links.failed_unlocks is from here the count of
-- wrong passphrases ever tried through the link (for the owner to see); locked_until is no longer
-- written (the lock is per client address, below).
--
-- share_unlock_failures: wrong passphrases per link and per client address bucket (an IPv4 address or
-- an IPv6 /64, hashed), with the time the bucket may try again, so a stranger cannot lock a link for
-- everyone.
--
-- notifications.count and share_id: the comments a guest writes in one thread within an hour are one
-- notification with a count, and notifications that came from a guest are kept apart so a flood of
-- guest comments cannot push a person's mentions out.
--
-- comments.client_key: a hash of what a guest sent (name, text, place), to answer a double submit
-- with the comment already written.

ALTER TABLE share_links ADD COLUMN whole INTEGER NOT NULL DEFAULT 1 CHECK (whole IN (0,1));
UPDATE share_links SET whole = CASE WHEN EXISTS (SELECT 1 FROM share_link_docs d WHERE d.share_id = share_links.id) THEN 0 ELSE 1 END;

CREATE TABLE share_unlock_failures (
  share_id     TEXT NOT NULL REFERENCES share_links(id) ON DELETE CASCADE,
  bucket       TEXT NOT NULL,
  failed       INTEGER NOT NULL DEFAULT 0,
  locked_until TEXT,
  last_at      TEXT NOT NULL,
  PRIMARY KEY (share_id, bucket)
) WITHOUT ROWID;

ALTER TABLE notifications ADD COLUMN count INTEGER NOT NULL DEFAULT 1;
ALTER TABLE notifications ADD COLUMN share_id TEXT;
CREATE INDEX notifications_share ON notifications(user_id, share_id, comment_id) WHERE share_id IS NOT NULL;

ALTER TABLE comments ADD COLUMN client_key TEXT;
CREATE INDEX comments_client_key ON comments(share_id, client_key) WHERE client_key IS NOT NULL;
