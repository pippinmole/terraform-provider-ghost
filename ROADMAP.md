# Roadmap

Tracks what the provider implements against the [Ghost API](https://api.ghost.procrastinator.fyi/reference)
(raw spec: [`/openapi.json`](https://api.ghost.procrastinator.fyi/openapi.json)). Items are ranked within each tier,
**top = implement first**.

| Status | Meaning |
| ------ | ------- |
| ✅ | Implemented |
| 🚧 | In progress |
| ❌ | Not implemented yet |
| 🚫 | Not possible (no endpoint the provider can reach) |

| Flag | Meaning |
| ---- | ------- |
| ⚠️ | Uses native endpoints that don't accept PATs yet. Can be implemented, but is untested until Ghost ships PAT support |

## API notes

> [!WARNING]
> **Native API auth (checked 2026-10-25):** personal access tokens currently work on `/api/v3` (`Authorization: Bearer …`
> or `token …`), `/api/uploads/*` and git, but **not** on Ghost's native `/api`, which today accepts only a browser session
> cookie (see `docs/0040-github-compatibility-is-a-translation-layer.md` in `ghost/ghost`). Only public native reads work
> with a PAT.
>
> The Ghost developer has confirmed that most native endpoints **will** accept PATs, so we build on that assumption:
> the client sends the same `Authorization: Bearer` header to both surfaces. Acceptance tests for ⚠️ items should skip
> with a clear message until then.

- **Prefer v3, fall back to native freely.** Mixing per operation is fine, e.g. create via v3, update via native.
- **Better Auth (`/api/auth/*`)** is the only way to create, rename or delete orgs and teams, change org member roles,
  cancel org invitations, or edit the user profile. It's not in OpenAPI and doesn't accept API keys, so those are 🚫.
- **Unknown body fields are silently stripped.** Never model a GitHub field Ghost doesn't have (homepage, topics,
  `has_*`, archived, merge settings…), or you get perpetual diffs.
- **Repository slugs aren't derived from names.** They're `slugify(name)`, plus a random suffix on collision, and they
  change on rename. Always store the slug the API returns and build URLs from it. Old owner/slug pairs redirect.
- PATs are rate-limited to **120 requests per minute**.

## Provider

| Status | Item | Notes |
| ------ | ---- | ----- |
| ✅ | Provider configuration | `token` (`GHOST_TOKEN`), `base_url` (`GHOST_BASE_URL`, default `https://api.ghost.procrastinator.fyi`) |
| ✅ | Typed API client (`internal/client`) | Supports both API surfaces, typed errors, `IsNotFound` |
| ❌ | Acceptance test harness | `TF_ACC` tests against live Ghost; skip helper for ⚠️ items; sweepers (need native repository delete ⚠️) |
| ❌ | First release on the Terraform Registry | Needs a GPG key and the `GPG_PRIVATE_KEY` / `PASSPHRASE` repo secrets |

## Tier 1: implement first

Core value: the repository and what people configure on it straight away.

| # | Status | Name | Kind | API | Notes |
| - | ------ | ---- | ---- | --- | ----- |
| 1 | ❌ | `ghost_repository` | resource | mixed | ⚠️ update and delete. Create: v3 `POST /user/repos` or `/orgs/{org}/repos` (`name`, `description`, `visibility`, `auto_init`, `gitignore_template`, `license_template`, the last three create-only). Read: v3 `GET /repos/{owner}/{repo}`. Update: native `PATCH /api/repositories/{u}/{slug}` (`name`, `description`, `visibility`). Delete: native `DELETE` (409 if it heads an open cross-repo PR). Import `owner/slug`. `owner` change = ForceNew, not a transfer. Default `visibility` is `private` |
| 2 | ❌ | `ghost_repository` | data source | v3 | `GET /repos/{owner}/{repo}` |
| 3 | ❌ | `ghost_user` | data source | v3 | `GET /user` (current) or `/users/{login}` |
| 4 | ❌ | `ghost_issue_label` | resource | v3 | Full CRUD on `/repos/{o}/{r}/labels[/{name}]`. Renames go through `new_name` in place. Strip a leading `#` from `color`, default `ededed`; name ≤ 50 chars, URL-encoded. Import `owner/repo:name` |
| 5 | ❌ | `ghost_repository_ruleset` | resource | v3 | Ghost's only branch protection. CRUD on `/repos/{o}/{r}/rulesets[/{id}]`. v3 `PUT` is **partial**, so always send the full object. Patterns come back as `refs/heads/…` (`~DEFAULT_BRANCH`, `~ALL` allowed). `bypass_actors` only returned to admins. Team `actor_id` is a numeric id the native team endpoints don't expose. Import `owner/repo:id` |
| 6 | ❌ | `ghost_release` | resource | v3 | CRUD on `/repos/{o}/{r}/releases[/{id}]`. `tag_name` is RequiresReplace (PATCH changes return 422). Don't model `make_latest`, `generate_release_notes`, `discussion_category_name`. Delete keeps the tag. Import `owner/repo:id` |
| 7 | ❌ | `ghost_branch` | resource | mixed | ⚠️ create and read. Create: native `POST /branches {name, from}`. Read: native `GET /branches` (list only, no SHA; check whether v3 `GET /git/refs/heads/{b}` gives a SHA). Delete: v3 `DELETE /git/refs/heads/{b}`. The source must exist, so no branches on empty repos. Import `owner/repo:branch` |
| 8 | ❌ | `ghost_branch_default` | resource | native | ⚠️ Native `PATCH /api/repositories/{u}/{slug} {defaultBranch}`; the branch must exist. Read from native `GET /branches`, because v3 `default_branch` says `main` even on empty repos. Delete just removes it from state. Import `owner/repo` |
| 9 | ❌ | `ghost_repository_webhook` | resource | native | ⚠️ `GET`/`POST …/webhooks`, then `PATCH`/`DELETE /{id}`; no single GET, so Read filters the list. Secret is server-generated and returned once (Computed + Sensitive, empty after import; `POST /{id}/secret` rotates it). No `content_type`/`insecure_ssl`. Validate `events` against Ghost's 37 event names. Ghost auto-disables failing hooks (`disabledReason`). Push events aren't delivered yet. Import `owner/repo:id` |

## Tier 2: access control and org management

| # | Status | Name | Kind | API | Notes |
| - | ------ | ---- | ---- | --- | ----- |
| 10 | ❌ | `ghost_repository_collaborator` | resource | native | ⚠️ `PUT …/collaborators/{user} {role}` invites the user or changes their role (idempotent); `DELETE` removes them or withdraws the invite. Read from the list (`status`: accepted/pending/expired). Invites expire after 7 days. Import `owner/repo:username` |
| 11 | ❌ | `ghost_team` | data source | native | ⚠️ unless the org is public. `GET /api/organizations/{slug}/teams/{team}` returns `id`, `members[].maintainer`, `repositories[]`. Lookup is by slug, which changes on rename |
| 12 | ❌ | `ghost_organization_teams` | data source | native | ⚠️ `GET /api/organizations/{slug}/teams`, members only |
| 13 | ❌ | `ghost_team_repository` | resource | native | ⚠️ `PUT`/`DELETE /api/repositories/{u}/{r}/teams/{teamId} {role}`; read from `GET …/teams`. Org-owned repos only. Uses the string team id. Import `owner/repo:teamId` |
| 14 | ❌ | `ghost_team_membership` | resource | native | ⚠️ `PUT`/`DELETE …/teams/{team}/members/{user}` and `…/maintainers/{user}`; `role` = `member`/`maintainer`. The user must already be in the org. Store the team id to survive renames. Import `org:team:username` |
| 15 | ❌ | `ghost_organization` | data source | native | `GET /api/organizations/{slug}` (public): profile, `members[]` (no roles), `pinned[]` |
| 16 | ❌ | `ghost_organization_settings` | resource | native | ⚠️ Singleton. `GET`/`PATCH /api/organizations/{slug}/settings`: `basePermission`, `membersCanCreatePublic/PrivateRepositories`, `allowPrivateForks`, `oauthAppRestrictions`, `defaultBranch`, `description`, `website`, `location`, `email`. Delete resets to defaults or drops from state (decide and document). Display name is Better Auth only. Import by org slug |
| 17 | ❌ | `ghost_organization_webhook` | resource | native | ⚠️ Same shape and gotchas as #9, on `/api/organizations/{slug}/webhooks`. Import `org:id` |
| 18 | ❌ | `ghost_user_ssh_key` | resource | native | ⚠️ v3 `/user/keys` returns `id: null` and has no delete, so use native `/api/ssh-keys` throughout (`publicKey`, `title`; returns `fingerprint`). Compare keys without the comment. Everything is ForceNew. Import `id` |
| 19 | ❌ | `ghost_user_ssh_signing_key` | resource | v3 | `POST`/`GET /user/ssh_signing_keys`, `GET`/`DELETE /{id}`. Everything is ForceNew. Import `id` |
| 20 | ❌ | `ghost_user_gpg_key` | resource | native | ⚠️ `GET`/`POST /api/gpg-keys {publicKey}`, `DELETE /{id}`. Everything is ForceNew. Import `id` |

## Tier 3: nice to have

| # | Status | Name | Kind | API | Notes |
| - | ------ | ---- | ---- | --- | ----- |
| 21 | ❌ | `ghost_oauth_app` | resource | native | ⚠️ User or org (`organization` is ForceNew). `POST /api/oauth-apps` or `/api/organizations/{slug}/oauth-apps`, then `GET`/`PATCH`/`DELETE /api/oauth-apps/{clientId}`. `clientSecret` is returned once. Import `clientId` |
| 22 | ❌ | `ghost_organization_oauth_app_access` | resource | native | ⚠️ `PUT /api/organizations/{slug}/oauth-app-access/{clientId} {state}`, `DELETE`. Only matters with `oauthAppRestrictions`. Import `org:clientId` |
| 23 | ❌ | `ghost_organization_pinned_repositories` | resource | native | ⚠️ `PUT /api/organizations/{slug}/pins` sets the whole ordered list (≤ 6). Read from the org profile. Delete = `PUT []`. Import by org slug |
| 24 | ❌ | `ghost_repositories` | data source | native | `GET /api/repositories/{username}`. Public reads work today; ⚠️ for private repos |
| 25 | ❌ | `ghost_branches` | data source | native | `GET …/branches` returns names and `defaultBranch`. ⚠️ for private repos |
| 26 | ❌ | `ghost_release` | data source | v3 | `/releases/latest`, `/releases/tags/{tag}`, `/releases/{id}` |
| 27 | ❌ | `ghost_gitignore_templates` | data source | v3 | `/gitignore/templates[/{name}]` |
| 28 | ❌ | `ghost_licenses` | data source | v3 | `/licenses[/{key}]` |
| 29 | ❌ | `ghost_repository_status_contexts` | data source | native | ⚠️ `GET …/status-contexts`, the 50 most recent; helps fill ruleset required checks |
| 30 | ❌ | `ghost_organization_members` | data source | native | From the public org profile. No roles, private members hidden |
| 31 | ❌ | `ghost_organization_invitation` | resource | native | ⚠️ Poor fit: native `POST /invitations` only; there's no list, and cancelling is Better Auth only. Create-only at best. Consider skipping until Ghost adds endpoints |

## Tier 4: later / niche

| # | Status | Name | Kind | API | Notes |
| - | ------ | ---- | ---- | --- | ----- |
| 32 | ❌ | `ghost_repository_subscription` | resource | native | ⚠️ `GET`/`PUT …/subscription {level}`. Per user; delete = `participating` |
| 33 | ❌ | `ghost_release_asset` | resource | mixed | Upload: `POST /api/uploads/repos/{o}/{r}/releases/{id}/assets` (raw bytes; a PAT works). Manage: v3 `/releases/assets/{id}`. ForceNew on content hash. Counts against storage quota |
| 34 | ❌ | `ghost_organization_logo` | resource | native | ⚠️ `PUT`/`DELETE /api/organizations/{slug}/logo` (raw PNG/JPEG) |
| 35 | ❌ | `ghost_organization_public_membership` | resource | native | ⚠️ `PUT`/`DELETE /public-members`, for yourself only |
| 36 | ❌ | `ghost_user_email` | resource | native | ⚠️ Poor fit: verification happens out of band |
| 37 | ❌ | `ghost_repository_rulesets` | data source | v3 | List rulesets |
| 38 | ❌ | `ghost_codeowners_errors` | data source | v3 | `/codeowners/errors` |
| 39 | ❌ | `ghost_storage_usage` | data source | native | ⚠️ `GET /api/storage/{owner}` |

## Not possible

| Status | Item | Why |
| ------ | ---- | --- |
| 🚫 | `ghost_organization` (create/rename/delete) | Better Auth only, not reachable with a PAT. Use the data source |
| 🚫 | `ghost_team` (create/rename/delete) | Better Auth only. Use the data source |
| 🚫 | `ghost_organization_membership` (roles, removal) | Better Auth only |
| 🚫 | User profile (name, avatar) | Better Auth only (`PUT /api/users/avatar` only uploads) |
| 🚫 | `ghost_repository_file` | No endpoint for writing file contents |
| 🚫 | Tags | No create endpoint (tags only come from releases) |
| 🚫 | Repo settings Ghost lacks | Topics, homepage, archived, `has_*`, merge settings, template repos |
| 🚫 | Deploy keys, Actions secrets/variables, environments, milestones, projects, org rulesets | Not in Ghost |
| 🚫 | Branch protection | Not in Ghost; use `ghost_repository_ruleset` |

## Deliberately excluded

These are transactional or ephemeral, so they're not a good fit for Terraform:

- Issues, pull requests, comments, reviews, merges (no delete)
- Stars, watching, notifications, commit statuses, attachments
- Forking and transfers as actions (maybe a `fork_source` create argument on `ghost_repository` later)
- Archives, search, GitHub imports, webhook pings and redeliveries
- Email verification, accepting invitations, revoking authorized apps, removing outside collaborators
- `/api/repository-templates`: same data as gitignore templates plus licenses
