-- 1874 mail: what the review asked for.
--
-- kindle_domains is the administrator's allow-list of Send-to-Kindle domains, comma separated and lower case; empty
-- means the default (kindle.com and free.kindle.com). It is never "any".
-- mail_digest.address is the account address a person opted in with: a digest stops when it changes.
-- kindle_sends.recipient_sealed is the full address the edition went to, sealed with the instance key and shown
-- only to the project's owner and to administrators, so abuse can be traced without the mailbox in the clear.
ALTER TABLE mail_config ADD COLUMN kindle_domains TEXT NOT NULL DEFAULT '';
ALTER TABLE mail_digest ADD COLUMN address TEXT NOT NULL DEFAULT '';
ALTER TABLE kindle_sends ADD COLUMN recipient_sealed TEXT NOT NULL DEFAULT '';
