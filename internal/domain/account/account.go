package account

import "github.com/crueber/scriobh/internal/ops"

// Register adds the account ops. Every one is Authenticated: a person acts on
// their own profile, preferences and sessions, and nothing else.
func Register(r *ops.Registry) {
	me := ops.Authenticated()
	ops.Register(r, ops.Op{
		Name: "profile.get", Method: "GET", Path: "/api/v1/me",
		Desc: "Get the caller's profile: name, email, instance role, time zone, locale, avatar colour and initials.", Auth: me,
	}, profileGet)
	ops.Register(r, ops.Op{
		Name: "profile.update", Method: "PATCH", Path: "/api/v1/me",
		Desc: "Change the caller's display name, time zone or locale. Send only the fields to change.", Auth: me,
	}, profileUpdate)
	ops.Register(r, ops.Op{
		Name: "profile.zone_default", Method: "POST", Path: "/api/v1/me/zone-default",
		Desc: "Store the browser's time zone for the caller, only if they have never chosen one. A zone already stored, chosen or from an earlier browser, is not changed. Returns the profile as it now is.", Auth: me,
	}, profileZoneDefault)
	ops.Register(r, ops.Op{
		Name: "prefs.get", Method: "GET", Path: "/api/v1/me/prefs",
		Desc: "Get the caller's preferences (theme, editor font, size, measure and line height, motion, autosave, density) with defaults filled in. Every value is a string.", Auth: me,
	}, prefsGet)
	ops.Register(r, ops.Op{
		Name: "prefs.set", Method: "PUT", Path: "/api/v1/me/prefs",
		Desc: "Set one or more preferences; keys not sent are left as they are. Values are strings, numbers included. An unknown key or invalid value fails the whole call and names the key. Returns every preference.", Auth: me,
	}, prefsSet)
	ops.Register(r, ops.Op{
		Name: "session.list", Method: "GET", Path: "/api/v1/me/sessions",
		Desc: "List the caller's live browser sessions, newest first, marking the current one. Never includes the session secret.", Auth: me,
	}, sessionList)
	ops.Register(r, ops.Op{
		Name: "session.revoke", Method: "POST", Path: "/api/v1/me/sessions/{sid}/revoke",
		Desc: "Sign out one of the caller's own sessions. Revoking the current session is allowed and signs the caller out. Another person's session is not found.", Auth: me,
	}, sessionRevoke)
	ops.Register(r, ops.Op{
		Name: "session.revoke_others", Method: "POST", Path: "/api/v1/me/sessions/revoke-others",
		Desc: "Sign out every live session of the caller except the current one; returns how many were ended.", Auth: me,
	}, sessionRevokeOthers)
}
