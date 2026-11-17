#!/usr/bin/env bash
# Sign in to a development server (make run, SCRIOBH_OIDC_STUB=1) as one of its three fixed test users and leave the
# session in a curl cookie jar. It walks the real OIDC code flow against the development issuer: the sign-in page,
# the picker's form, the callback. Used by the examples in docs/API.md.
#
#   scripts/dev-login.sh http://127.0.0.1:PORT jar.txt [dev-writer | dev-admin | dev-reader]
#   curl -b jar.txt http://127.0.0.1:PORT/api/v1/_whoami
#
# A production build has no development issuer; there, sign in with a browser and make an API key.
set -euo pipefail
if [ $# -lt 2 ]; then echo "usage: $0 BASE_URL COOKIE_JAR [dev-writer|dev-admin|dev-reader]" >&2; exit 2; fi
base=${1%/} jar=$2 sub=${3:-dev-writer}
page=$(curl -sS -c "$jar" -b "$jar" -L "$base/auth/login")
case $page in *"Development sign-in"*) ;; *) echo "dev-login: $base did not show the development issuer's picker (is it a make run server?)" >&2; exit 1 ;; esac
# every field of the picker's forms is the same but sub, so read them from the first
val() { printf '%s' "$page" | grep -o "name=\"$1\" value=\"[^\"]*\"" | head -1 | sed 's/.*value="//; s/"$//; s/&amp;/\&/g; s/&#34;/"/g'; }
action=$(printf '%s' "$page" | grep -o '<form method="post" action="[^"]*"' | head -1 | sed 's/.*action="//; s/"$//; s/&amp;/\&/g')
code=$(curl -sS -o /dev/null -w '%{http_code}' -c "$jar" -b "$jar" -L "$action" \
  --data-urlencode "sub=$sub" \
  --data-urlencode "client_id=$(val client_id)" --data-urlencode "redirect_uri=$(val redirect_uri)" \
  --data-urlencode "state=$(val state)" --data-urlencode "nonce=$(val nonce)" \
  --data-urlencode "code_challenge=$(val code_challenge)" --data-urlencode "code_challenge_method=$(val code_challenge_method)" \
  --data-urlencode "response_type=$(val response_type)")
[ "$code" = 200 ] || { echo "dev-login: the callback ended at HTTP $code" >&2; exit 1; }
echo "signed in as $sub; session in $jar"
