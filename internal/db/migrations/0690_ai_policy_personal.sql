-- 0690 personal providers need the owner's say-so. A project's policy with an empty
-- provider list used to mean "any provider the person may use", which included a
-- personal provider on a host the person controls. Now an empty list means the
-- instance's providers only; personal ones are allowed only when the owner turns
-- this on (or names one of their own in the list).

ALTER TABLE ai_policies ADD COLUMN allow_personal_providers INTEGER NOT NULL DEFAULT 0;
