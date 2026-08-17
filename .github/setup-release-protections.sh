#!/usr/bin/env bash
#
# Applies the two release protections that .github/workflows/release.yml relies
# on but cannot contain.
#
# Why this file exists: a workflow can declare `environment: release`, and GitHub
# will happily create that environment with no protection rules at all the first
# time the job runs. It can also do nothing whatsoever about who is allowed to
# push the tag that starts it. So the two controls that actually stop an
# unreviewed release live in repository settings — outside the repository,
# invisible to review, and lost the moment someone forks or migrates. This script
# puts them back under version control as a procedure, and reads the result back
# afterwards so it can be checked rather than assumed.
#
# usage:
#   .github/setup-release-protections.sh [OPTIONS]
#
# options:
#   --repo OWNER/NAME    default: the repo of the current directory
#   --reviewer LOGIN     required reviewer for the release environment
#                        (repeatable; default: the repository owner)
#   --restrict-creation  also restrict *creating* v* tags to the bypass actor.
#                        Off by default — see the comment above the rules.
#   --dry-run            print every request body, send nothing
#
# Re-running is safe: every step is a create-or-update.

set -euo pipefail

RULESET_NAME='release tags'
ENVIRONMENT='release'
TAG_PATTERN='v*'

repo=''
reviewer_logins=''   # space separated; GitHub logins cannot contain spaces
restrict_creation=false
dry_run=false

die() { printf 'error: %s\n' "$*" >&2; exit 1; }
note() { printf '\n\033[1m%s\033[0m\n' "$*"; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --repo)              repo="${2:?--repo needs a value}"; shift 2 ;;
    --reviewer)          reviewer_logins+=" ${2:?--reviewer needs a value}"; shift 2 ;;
    --restrict-creation) restrict_creation=true; shift ;;
    --dry-run)           dry_run=true; shift ;;
    -h|--help)           sed -n '2,27p' "$0" | sed 's/^#\{0,1\} \{0,1\}//'; exit 0 ;;
    *)                   die "unknown option: $1" ;;
  esac
done

command -v gh >/dev/null || die 'the GitHub CLI (gh) is required'
command -v jq >/dev/null || die 'jq is required'
gh auth status >/dev/null 2>&1 || die 'gh is not authenticated — run: gh auth login'

if [[ -z $repo ]]; then
  repo=$(gh repo view --json nameWithOwner -q .nameWithOwner 2>/dev/null) \
    || die 'could not determine the repository — pass --repo OWNER/NAME'
fi

# Fail early and clearly rather than half way through. A repository that does not
# exist yet is the ordinary case for a first run.
repo_json=$(gh api "repos/$repo" 2>/dev/null) \
  || die "repository $repo not found, or this token cannot see it"
owner=$(jq -r '.owner.login' <<<"$repo_json")

if [[ $(jq -r '.private' <<<"$repo_json") == true ]]; then
  cat >&2 <<'EOF'
warning: this repository is private.

  Environment protection rules and repository rulesets are free on public
  repositories; on private ones they need a paid plan (GitHub Pro for a personal
  account, Team or Enterprise for an organisation). A 403 or an "upgrade" message
  below is that, not a bug in this script.

EOF
fi

[[ -n ${reviewer_logins// /} ]] || reviewer_logins=$owner

send() { # send <description> <method> <path> [json body]
  local desc="$1" method="$2" path="$3" body="${4-}"
  if [[ $dry_run == true ]]; then
    printf '  would %s %s\n' "$method" "$path"
    [[ -n $body ]] && jq . <<<"$body" | sed 's/^/    /'
    return 0
  fi
  if [[ -n $body ]]; then
    gh api --method "$method" "$path" --input - <<<"$body" >/dev/null
  else
    gh api --method "$method" "$path" >/dev/null
  fi
  printf '  %s\n' "$desc"
}

# ---------------------------------------------------------------------------
# 1. The release environment: a human has to approve before anything publishes.
# ---------------------------------------------------------------------------
note "1. environment '$ENVIRONMENT' on $repo"

reviewer_json='[]'
reviewer_count=0
for login in $reviewer_logins; do
  id=$(gh api "users/$login" --jq '.id' 2>/dev/null) || die "no such GitHub user: $login"
  reviewer_json=$(jq --argjson id "$id" '. + [{type: "User", id: $id}]' <<<"$reviewer_json")
  reviewer_count=$((reviewer_count + 1))
  printf '  reviewer: %s (id %s)\n' "$login" "$id"
done

# prevent_self_review stays off when there is exactly one reviewer. With a single
# reviewer who is also the person pushing tags — the normal case for a solo
# maintainer — turning it on makes the release unapprovable by anybody, so the
# gate would not be strict, it would be broken. What remains is still the control
# that matters: publishing needs a human to click approve, which a stolen token
# pushing a tag cannot do by itself.
prevent_self_review=false
[[ $reviewer_count -gt 1 ]] && prevent_self_review=true
printf '  prevent_self_review: %s\n' "$prevent_self_review"

env_body=$(jq -n \
  --argjson reviewers "$reviewer_json" \
  --argjson prevent "$prevent_self_review" \
  '{
     wait_timer: 0,
     prevent_self_review: $prevent,
     reviewers: $reviewers,
     deployment_branch_policy: {
       protected_branches: false,
       custom_branch_policies: true
     }
   }')
send 'environment configured' PUT "repos/$repo/environments/$ENVIRONMENT" "$env_body"

# custom_branch_policies above only means "restricted to a list"; the list itself
# is a second call. Until it exists the environment is restricted to nothing,
# so this is not optional polish.
policy_count=0
if [[ $dry_run == false ]]; then
  policy_count=$(gh api "repos/$repo/environments/$ENVIRONMENT/deployment-branch-policies" \
    --jq "[.branch_policies[] | select(.name == \"$TAG_PATTERN\" and .type == \"tag\")] | length")
fi
if [[ $policy_count == 0 ]]; then
  send "restricted to tags matching $TAG_PATTERN" POST \
    "repos/$repo/environments/$ENVIRONMENT/deployment-branch-policies" \
    "$(jq -n --arg name "$TAG_PATTERN" '{name: $name, type: "tag"}')"
else
  printf '  restricted to tags matching %s (already set)\n' "$TAG_PATTERN"
fi

# ---------------------------------------------------------------------------
# 2. The tag ruleset: a released tag can never be repointed.
# ---------------------------------------------------------------------------
note "2. ruleset '$RULESET_NAME' on refs/tags/$TAG_PATTERN"

# `update` and `deletion` carry the weight here, and they are on unconditionally
# because they cannot break a release: a published tag must not be movable or
# removable afterwards, or "v1.2.3" stops naming one specific commit and the
# provenance attestation stops meaning anything.
#
# `creation` is opt-in. Restricting it only does something once somebody besides
# the owner has write access, and getting the bypass actor wrong locks the owner
# out of tagging — which you would find out at release time.
rules=$(jq -n '[{type: "update"}, {type: "deletion"}]')
bypass='[]'
if [[ $restrict_creation == true ]]; then
  owner_id=$(gh api "users/$owner" --jq '.id')
  rules=$(jq -n --argjson r "$rules" '$r + [{type: "creation"}]')
  bypass=$(jq -n --argjson id "$owner_id" \
    '[{actor_type: "User", actor_id: $id, bypass_mode: "always"}]')
  printf '  creation restricted; bypass: %s (id %s)\n' "$owner" "$owner_id"
fi

ruleset_body=$(jq -n \
  --arg name "$RULESET_NAME" \
  --arg pattern "refs/tags/$TAG_PATTERN" \
  --argjson rules "$rules" \
  --argjson bypass "$bypass" \
  '{
     name: $name,
     target: "tag",
     enforcement: "active",
     bypass_actors: $bypass,
     conditions: {ref_name: {include: [$pattern], exclude: []}},
     rules: $rules
   }')

ruleset_id() {
  gh api "repos/$repo/rulesets" \
    --jq ".[] | select(.name == \"$RULESET_NAME\") | .id" 2>/dev/null | head -1
}

existing_id=''
[[ $dry_run == false ]] && existing_id=$(ruleset_id)
if [[ -n $existing_id ]]; then
  send "ruleset updated (id $existing_id)" PUT "repos/$repo/rulesets/$existing_id" "$ruleset_body"
else
  send 'ruleset created' POST "repos/$repo/rulesets" "$ruleset_body"
fi

# ---------------------------------------------------------------------------
# 3. Read it back. A setup script that only reports what it sent repeats the
#    mistake of a CI job that prints a number and passes regardless.
# ---------------------------------------------------------------------------
if [[ $dry_run == true ]]; then
  note 'dry run — nothing was sent'
  exit 0
fi

note "applied to $repo"

gh api "repos/$repo/environments/$ENVIRONMENT" --jq '
  (.protection_rules[] | select(.type == "required_reviewers")) as $r
  | "  required reviewers: " +
      (if ($r.reviewers // []) | length == 0 then "NONE — the gate is not active"
       else [$r.reviewers[].reviewer.login] | join(", ") end),
    "  prevent self review: \($r.prevent_self_review)"'
gh api "repos/$repo/environments/$ENVIRONMENT/deployment-branch-policies" --jq '
  "  may deploy from: " +
    (if (.branch_policies // []) | length == 0 then "anything"
     else [.branch_policies[] | "\(.type) \(.name)"] | join(", ") end)'

final_id=$(ruleset_id)
if [[ -z $final_id ]]; then
  die "the ruleset is not present after the call — nothing is protecting refs/tags/$TAG_PATTERN"
fi
gh api "repos/$repo/rulesets/$final_id" --jq '
  "  ruleset: \(.name) [\(.enforcement)]",
  "    applies to: \(.conditions.ref_name.include | join(", "))",
  "    rules: \([.rules[].type] | join(", "))",
  "    bypass: " + (if (.bypass_actors // []) | length == 0 then "nobody"
                   else [.bypass_actors[] | "\(.actor_type) \(.actor_id)"] | join(", ") end)'

cat <<'EOF'

  One thing no API call can check for you: GitHub asks for the approval only when
  a job actually references the environment, so confirm the release job still
  carries `environment: release`. Without that line the environment above is
  decoration.
EOF
