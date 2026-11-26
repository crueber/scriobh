-- 0111 whether a person has chosen their time zone (U5). The zone column defaults to UTC, so UTC alone cannot tell "never chosen"
-- from "chose UTC": timezone_set does. A person who has never chosen one gets the browser's zone the first time the writing day
-- needs it (profile.zone_default), and a zone that is stored is never written over.
-- Existing rows: a zone that is empty or the default (UTC) is treated as unset, so the browser's zone can be stored for it; any other
-- zone is a choice and is marked set. This means a person who deliberately chose UTC before this migration gets their browser's zone
-- once (a trade-off recorded in docs/OPEN-QUESTIONS.md).
ALTER TABLE users ADD COLUMN timezone_set INTEGER NOT NULL DEFAULT 0;
UPDATE users SET timezone_set = 1 WHERE timezone IS NOT NULL AND timezone <> '' AND timezone <> 'UTC';
