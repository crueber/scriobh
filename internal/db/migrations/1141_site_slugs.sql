-- 1141 site slugs: the address a document was given the first time the site was
-- built or published, kept so that it does not move.
--
-- Without it an address made from a title depends on which other documents
-- exist: a newer post with the same title, an older one finished later, or an
-- address someone chooses by hand would move a post that is already out. The
-- generator uses a pinned address before it makes a new one. A row may now exist
-- for a document with no settings of its own (role auto, nothing chosen), only to
-- hold its pinned address.

ALTER TABLE site_docs ADD COLUMN pinned_slug TEXT NOT NULL DEFAULT '';
