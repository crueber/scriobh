package gitmirror

import (
	"context"
	"strings"

	"github.com/crueber/scriobh/internal/ops"
)

// Who may widen what reaches a repository that people can read.
//
// A public remote is the one place where the full mirror must never go, and the owner's API key is the one
// credential an agent can hold that is not a person at a browser. So a change that could put more on such a remote
// (the audience, the notes, only_done, the address) and a force push of it need a person signed in with a browser.
// A remote is *exposed* when its audience is public, when repo.publish made it, or when its address is one the
// project has ever given a public remote (git_public_addresses, which outlives the remote: deleting a public
// remote and making a private one at the same address is the same change). The rule is refused at the op, and the
// push re-checks the host's own visibility where an API can say it (see checkVisibility).
//
// What this does not cover: an SSH or custom remote has no API to ask, so for those only the session rule stands.

// CodePublicRepository is the refusal to push the full mirror to a repository the host says is public.
const CodePublicRepository = "repository_is_public"

func needsPerson(c *ops.Ctx) error {
	if c.P.Via == "apikey" {
		return ops.Forbidden("This changes what reaches a repository people can read (or may have been made public). It needs a person signed in with a browser; an API key cannot do it.")
	}
	return nil
}

func (s *Service) addrOf(raw string) (endpointInfo, bool) {
	ep, e := parseURL(raw, true)
	return ep, e == nil
}

// exposed reports whether a remote is, or sits at the address of, something public.
func (s *Service) exposed(ctx context.Context, r *remoteRow) bool {
	if r.Audience != AudiencePrivate || r.CreatedVia == "publish" {
		return true
	}
	ep, ok := s.addrOf(r.URL)
	return ok && s.everPublic(ctx, r.ProjectID, ep)
}

// recordPublic remembers the address of a remote that is public.
func (s *Service) recordPublic(ctx context.Context, r *remoteRow) {
	if r.Audience == AudiencePrivate {
		return
	}
	if ep, ok := s.addrOf(r.URL); ok {
		s.markPublic(ctx, r.ProjectID, ep)
	}
}

// checkVisibility asks the host, for a private remote at GitHub or a Forgejo or Gitea host with a token, whether the
// repository has become public. A public repository gets no full mirror: the push is refused (the history already
// holds the rest, so the filter is not switched on behind the owner's back), the address is marked, and the owner
// decides. A failed question fails the push retryably. It is one request to the host's API per push of such a remote.
func (s *Service) checkVisibility(ctx context.Context, r *remoteRow, cr *credentials) *Error {
	if r.Audience != AudiencePrivate || r.AuthKind != AuthHTTPS || cr == nil || cr.token == "" || (r.Preset != ProviderGitHub && r.Preset != ProviderForgejo) {
		return nil
	}
	ep, e := parseURL(r.URL, false)
	if e != nil {
		return nil
	}
	host := ep.Host
	if ep.Port != 443 {
		host = ep.HostPort()
	}
	t, e := newTarget(r.Preset, host)
	parts := strings.Split(strings.TrimSuffix(strings.Trim(ep.Path, "/"), ".git"), "/")
	if e != nil || len(parts) != 2 || !validOwner(parts[0]) || !validRepoName(parts[1]) {
		return nil
	}
	info, exists, he := newProviderClient(t, cr.token).repo(ctx, parts[0], parts[1])
	if he != nil {
		return he
	}
	if exists && !info.Private {
		s.markPublic(ctx, r.ProjectID, ep)
		return gerr(CodePublicRepository, "%s says this repository is public, and this remote carries the whole copy (notes, alternatives, outline details). Nothing was pushed. Switch the remote to what a reader sees (its audience), or make the repository private at the host.", ep.Host)
	}
	return nil
}
