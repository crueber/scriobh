// Package mail is Scríobh's one outbound mail path: an SMTP submission client, a safe message builder, a
// durable outbox, the Send to Kindle delivery and the email digest.
//
// Mail is an operator-configured, off-by-default service. Nothing is sent unless an administrator switched
// mail on and gave a relay, and a person switched on the one feature that sends: a Kindle delivery they asked
// for (and previewed) or the digest they opted in to. No message carries manuscript text beyond the edition a
// person chose to send to their own Kindle, and a digest carries counts and project titles only. The privacy
// invariant ("no outbound request the user did not configure") holds because the operator configures the
// relay and each person opts in per feature; see docs/p7-notes/mail.md.
//
// Failures are kept as a class (Class), never as the text a relay said: a hostile server can echo what it was
// sent. The relay password is sealed with internal/secrets and never leaves the opened scope of one send.
package mail
