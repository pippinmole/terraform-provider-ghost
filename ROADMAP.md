# Roadmap

Tracks what the provider implements against the [Ghost API](https://api.ghost.procrastinator.fyi/reference)
(raw spec: [`/openapi.json`](https://api.ghost.procrastinator.fyi/openapi.json)).

| Status | Meaning |
| ------ | ------- |
| ✅ | Implemented |
| 🚧 | In progress |
| 📋 | Planned |
| ❌ | Not supported (the API has no endpoint for it) |
| ⚠️ | Uses native endpoints that don't accept PATs yet: can be implemented, but is untested until Ghost ships PAT support |

**API surface:** we prefer the GitHub-compatible API (`/api/v3`) and fall back to the native API (`/api`)
where v3 doesn't cover an operation. The **API** column shows which one each item uses.

> [!WARNING]
> **Native API auth (checked 2026-10-25):** personal access tokens currently work on `/api/v3` (`Authorization: Bearer …`
> or `token …`) and git, but **not** on Ghost's native `/api`, which today accepts only a browser session cookie
> (see `docs/0040-github-compatibility-is-a-translation-layer.md` in `ghost/ghost`). Only public native reads work with a PAT.
>
> The Ghost developer has confirmed that most native endpoints **will** accept PATs, so we build on that assumption:
> the client sends the same `Authorization: Bearer` header to both surfaces. Items marked ⚠️ depend on native PAT support.
> They can be implemented now, but they **cannot be acceptance-tested** until Ghost ships it. Their acceptance tests should
> skip with a clear message until then.
>
> PATs are rate-limited to 120 requests per minute.

## Provider

| Status | Item | Notes |
| ------ | ---- | ----- |
| ✅ | Provider configuration | `token` (`GHOST_TOKEN`), `base_url` (`GHOST_BASE_URL`, default `https://api.ghost.procrastinator.fyi`) |
| ✅ | Typed API client (`internal/client`) | Supports both API surfaces, typed errors, `IsNotFound` |
| 📋 | Acceptance test harness | `TF_ACC` tests against live Ghost, sweepers for leaked resources |
| 📋 | First release on the Terraform Registry | Needs a GPG key and the `GPG_PRIVATE_KEY` / `PASSPHRASE` repo secrets |

## Resources

| Status | Resource | API | Notes |
| ------ | -------- | --- | ----- |
| 📋 | `ghost_repository` | v3 + native | Create: v3 `POST /user/repos`, `POST /orgs/{org}/repos`. Read: v3. Update/delete: native only (`PATCH`/`DELETE /api/repositories/{user}/{slug}`) ⚠️ blocked by native auth |
| 📋 | `ghost_repository_ruleset` | v3 | Full CRUD on `/repos/{owner}/{repo}/rulesets` |
| 📋 | `ghost_issue_label` | v3 | Full CRUD on `/repos/{owner}/{repo}/labels` |
| 📋 | `ghost_release` | v3 | Full CRUD on `/repos/{owner}/{repo}/releases` |
| 📋 | `ghost_user_ssh_signing_key` | v3 | Create/read/delete |
| 📋 | `ghost_user_ssh_key` | v3 + native | Create/read: v3 `/user/keys`. Delete: native `DELETE /api/ssh-keys/{id}` ⚠️ |
| 📋 | `ghost_branch` | native + v3 | Create: native `POST /branches` ⚠️. Delete: v3 `DELETE /git/refs/{ref}` |
| 📋 | `ghost_repository_collaborator` | native | `PUT`/`DELETE /collaborators/{user}` ⚠️ |
| 📋 | `ghost_repository_team` | native | `PUT`/`DELETE /teams/{teamId}` ⚠️ |
| 📋 | `ghost_repository_webhook` | native | CRUD on `/repositories/{user}/{repo}/webhooks` ⚠️ |
| 📋 | `ghost_repository_subscription` | native | `GET`/`PUT /subscription` ⚠️ |
| 📋 | `ghost_user_gpg_key` | native | Create/delete on `/api/gpg-keys` ⚠️ |
| 📋 | `ghost_organization_settings` | native | `GET`/`PATCH /organizations/{slug}/settings` ⚠️ |
| 📋 | `ghost_organization_webhook` | native | CRUD on `/organizations/{slug}/webhooks` ⚠️ |
| 📋 | `ghost_team_membership` | native | `PUT`/`DELETE /teams/{team}/members/{user}` and `/maintainers/{user}` ⚠️ |
| 📋 | `ghost_organization_oauth_app_access` | native | `PUT`/`DELETE /oauth-app-access/{clientId}` ⚠️ |
| 📋 | `ghost_oauth_app` | native | CRUD on `/api/oauth-apps` ⚠️ |
| ❌ | `ghost_organization` (create/delete) | — | No create/delete endpoint, read-only |
| ❌ | `ghost_team` (create/delete) | — | No create/delete endpoint, read-only |
| ❌ | `ghost_repository_file` | — | No endpoint for writing file contents |
| ❌ | Branch protection | — | Use `ghost_repository_ruleset` instead |

## Data sources

| Status | Data source | API | Notes |
| ------ | ----------- | --- | ----- |
| 📋 | `ghost_user` | v3 | `/users/{login}`; the current user comes from `/user` |
| 📋 | `ghost_repository` | v3 | `/repos/{owner}/{repo}` |
| 📋 | `ghost_repositories` | native | `/api/repositories/{user}` (public) |
| 📋 | `ghost_organization` | native | `/api/organizations/{slug}` |
| 📋 | `ghost_organization_teams` / `ghost_team` | native | `/organizations/{slug}/teams[/{team}]` |
| 📋 | `ghost_branches` | native | `/repositories/{user}/{slug}/branches` |
| 📋 | `ghost_release` (latest / by tag) | v3 | `/releases/latest`, `/releases/tags/{tag}` |
| 📋 | `ghost_gitignore_templates` | v3 | `/gitignore/templates` |
| 📋 | `ghost_licenses` | v3 | `/licenses` |
| 📋 | `ghost_repository_templates` | native | `/api/repository-templates` |
