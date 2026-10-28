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

## API notes

> [!NOTE]
> **Native API auth (checked 2026-10-25):** personal access tokens work on both `/api/v3` and Ghost's native `/api`
> (`Authorization: Bearer …`), as well as `/api/uploads/*` and git. The client sends the same header to both surfaces.

- **Prefer v3, fall back to native freely.** Mixing per operation is fine, e.g. create via v3, update via native.
- **Better Auth (`/api/auth/*`)** is the only way to create, rename or delete orgs and teams, change org member roles,
  cancel org invitations, or edit the user profile. It's not in OpenAPI and doesn't accept API keys, so those are 🚫.
- **Unknown body fields are silently stripped.** Never model a GitHub field Ghost doesn't have (homepage, topics,
  `has_*`, archived, merge settings…), or you get perpetual diffs.
- **Repository slugs aren't derived from names.** They're `slugify(name)`, plus a random suffix on collision, and they
  change on rename. Always store the slug the API returns and build URLs from it. Old owner/slug pairs redirect.
- PATs are rate-limited to **120 requests per minute**, in a fixed window shared by both surfaces. Every response
  carries `X-RateLimit-*` headers, and a request over the limit gets a `429` with `Retry-After`. The client waits
  out the limit and retries (any method, since a `429` request is never processed).

## Provider

| Status | Item | Notes |
| ------ | ---- | ----- |
| ✅ | Provider configuration | `token` (`GHOST_TOKEN`), `base_url` (`GHOST_BASE_URL`, default `https://api.ghost.procrastinator.fyi`) |
| ✅ | Typed API client (`internal/client`) | Supports both API surfaces, typed errors, `IsNotFound` |
| 🚧 | Acceptance test harness | `testAccPreCheck` and resource/data source acceptance tests exist (`TF_ACC` against live Ghost); ❌ sweepers for orphaned `tf-acc-*` repositories |
| ❌ | First release on the Terraform Registry | Needs a GPG key and the `GPG_PRIVATE_KEY` / `PASSPHRASE` repo secrets |

## Tier 1: implement first

Core value: the repository and what people configure on it straight away.

| # | Status | Name | Kind | API | Notes |
| - | ------ | ---- | ---- | --- | ----- |
| 1 | ✅ | `ghost_repository` | resource | mixed | Create: v3 `POST /user/repos` or `/orgs/{org}/repos` (`name`, `description`, `visibility`, `auto_init`, `gitignore_template`, `license_template`, the last three create-only). Read: v3 `GET /repos/{owner}/{repo}`. Update: native `PATCH /api/repositories/{u}/{slug}` (`name`, `description`, `visibility`, `default_branch`). Delete: native `DELETE` (409 if it heads an open cross-repo PR). Import `owner/slug`. `owner` change = ForceNew, not a transfer. Default `visibility` is `private` |
| 2 | ✅ | `ghost_repository` | data source | v3 | `GET /repos/{owner}/{repo}` |
| 3 | ✅ | `ghost_user` | data source | v3 | `GET /user` (current) or `/users/{login}`. Logins are matched case-sensitively |
| 4 | ✅ | `ghost_issue_label` | resource | v3 | Full CRUD on `/repos/{o}/{r}/labels[/{name}]`. Renames go through `new_name` in place. `color` accepts a leading `#` and any case (Ghost stores lower case without `#`; the configured spelling is kept), default `ededed`. Names are ≤ 50 chars, URL-encoded and case-sensitive; Ghost trims surrounding whitespace, so it is rejected, as are `.` and `..`. Import `owner/repo:name` (split at the first `:`) |
| 5 | ✅ | `ghost_repository_ruleset` | resource | native | Ghost's only branch protection, shaped like the GitHub provider's. Native API (`/api/repositories/{o}/{r}/rulesets`), whose `PUT` is a full replace. Patterns are stored as `refs/heads/…` / `refs/tags/…` (`~DEFAULT_BRANCH`, `~ALL` allowed); the configured spelling is kept. Bypass actors: repository role, organization admin, or team by its string team id. Import `owner/repo:ruleset_id` |
| 6 | ✅ | `ghost_branch` | resource | mixed | Create: native `POST /branches {name, from}` (`from` is a branch, tag, full or short SHA, or `refs/heads/…`; defaults to the default branch; 404 "Nothing to branch from" for a missing ref or an empty repo; 409 if it exists). Read: native `GET /branches` for existence (list only, no SHA), then v3 `GET /commits/refs%2Fheads%2F{branch}/status` for `sha` (works with no statuses; the full ref stops a same-named tag shadowing the branch). Delete: native `DELETE /branches/{b}` (percent-encoded; 404 if gone; 409 for the default branch or a branch an open PR compares). `branch` is ForceNew (no rename API); `source` is create-only. `sha` follows pushes. Git over HTTPS accepts the PAT as Basic auth, which the acceptance tests use to push. Import `owner/repo:branch` |
| 7 | ✅ | `ghost_branch_default` | resource | native | Set: native `PATCH /api/repositories/{u}/{slug} {defaultBranch}` (404 "Branch not found" for a missing branch, a full `refs/heads/…` ref, or an empty repo); Create checks the branch list first for a clear error. Read: native `GET /branches` `defaultBranch`, which is git `HEAD` (v3 and native repo `default_branch` keep the stored setting, which says `main` on empty repos and goes stale if the default branch is deleted over git, where HEAD falls back to another branch). Empty repo (`defaultBranch: null`) or gone → removed from state. Delete restores `previous_branch` (the default before Create; null after import, then Delete only drops state), so a referenced `ghost_branch` can be deleted afterwards (Ghost 409s deleting the default branch). Moving the default off a `ghost_branch` and removing it needs two applies: Terraform destroys the branch before updating its dependent. Conflicts with `ghost_repository.default_branch`: use one or the other. Repository rename is in place. Import `owner/repo` |
| 8 | ✅ | `ghost_repository_webhook` | resource | native | `GET`/`POST /api/repositories/{u}/{r}/webhooks`, `PATCH`/`DELETE …/{id}`; no single GET, so Read filters the list (IDs look like `whk_…`). POST ignores `active` (a new hook is always on), so `active = false` is a PATCH after create. PATCH is partial, and an empty `{}` PATCH returns 500, so the provider never sends one. Events are de-duplicated and kept in the order sent (modelled as a set, validated against the 37 names); the URL is stored verbatim. Ghost rejects (400) non-http(s) schemes, userinfo, URLs over 2048 chars, `localhost`/private/reserved addresses and hosts that don't resolve; `http://` is allowed. `secret` (`whsec_…`) is returned only by create and `POST …/{id}/secret` (rotate, via `secret_rotation`): Computed + Sensitive, null after import. Deliveries carry `X-Hub-Signature-256` = `sha256=` + hex HMAC-SHA256(secret, body), and `GET …/{id}/deliveries` shows the signed body and headers, so the acceptance tests prove the secret. `https://web.ghost.procrastinator.fyi/` answers POST with 200, a first-party URL for successful deliveries. Ghost auto-disables failing hooks (`disabled_reason`); the next apply turns them back on. `url` is Sensitive. Client type `client.Webhooks` is shared with #16. Import `owner/repo:id` |

## Tier 2: access control and org management

| # | Status | Name | Kind | API | Notes |
| - | ------ | ---- | ---- | --- | ----- |
| 9 | ✅ | `ghost_repository_collaborator` | resource | native | Native only (v3 has no collaborator routes). `PUT /api/repositories/{o}/{r}/collaborators/{user} {role}` invites the user or changes their role (200 returns the collaborator; idempotent upsert; a role change keeps `invitedAt`/`expiresAt`; an expired invite is sent again). `DELETE` removes an accepted collaborator **or withdraws a pending invite** (204; 404 "X is not a collaborator on this repository" when gone, which Delete ignores). Read filters `GET …/collaborators` (`{username, name, image, role, status, invitedAt, expiresAt}`); no single GET. `status` is `pending`/`accepted`/`expired`: pending is a valid end state (exposed with `invited_at`/`expires_at`); expired or declined/withdrawn → removed from state, so the next apply invites again. Invites expire after 7 days; `expiresAt` is null once accepted. Usernames are matched case-insensitively (the configured spelling is kept; the ID uses Ghost's lower case); roles are case-sensitive and a bad one is a 400 whose `message` is an array. Unknown user → 404 "User X not found"; inviting the repo's user owner → 400 "The owner already has every permission on this repository". Oddity: inviting an org owner to the org's own repo creates a pending invite to themselves. On org repos a non-member is listed under `/api/organizations/{slug}/outside-collaborators` (`pending: true`) until accepted or withdrawn. Invites survive a repository rename (old slugs redirect), so `repository` renames are in place; deleting the repository deletes its invites (use `replace_triggered_by`). Acceptance and expiry can't be tested live (no second token): the acceptance tests leave the invite pending. Import `owner/repo:username` |
| 10 | ✅ | `ghost_team` | data source | native | `GET /api/organizations/{slug}/teams/{team}` returns `id`, `name`, `slug`, `members[]` (`username`, `name`, nullable `image`, `maintainer`), `repositories[]` (`slug`, `name`, `visibility`, `role`; no owner, it's the org) and `viewerCanManage`. Org and team slugs are matched case-insensitively. Lookup by `slug` or `id`: there's no GET by id (it 404s "Team not found"), so `id` lists `/teams` and then GETs by its slug. The id is stable across renames and is what `team_id` arguments take. 404 "Organization not found: X" for an unknown org. Teams can't be created with a PAT, so acceptance tests read an existing team (`GHOST_TEST_ORGANIZATION`). Shared model for #11: `client.ListOrganizationTeams`/`TeamSummary`, `internal/provider/team_model.go` |
| 11 | ✅ | `ghost_organization_teams` | data source | native | `GET /api/organizations/{slug}/teams` returns `{teams:[{id, name, slug, memberCount, repositoryCount}]}`, with no pagination (query parameters are ignored). The org slug is matched case-insensitively. Members only: an org the token user isn't in (e.g. `ghost`), a user login and an unknown org all return 404 "Organization not found: X", so the error hints at membership. Exposes `teams` sorted by slug, with `member_count`/`repository_count`; `id` is the org as configured. No members or repositories per team (that would be one GET per team): use `ghost_team` by `id` for those |
| 12 | ✅ | `ghost_team_repository` | resource | native | `PUT /api/repositories/{org}/{repo}/teams/{teamId} {role}` gives a team a role or changes it (204, idempotent); `DELETE` removes it (404 "Team not found in this organization" when the team has no access, which Delete ignores). Read from `GET …/teams`, which lists every team in the org with `{id, name, memberCount, role}` (`role: null` = no access) and no slug, so Read also lists the org's teams for `team_slug`. Roles: `read`, `triage`, `write`, `maintain`, `admin` (case-sensitive; anything else is a 400). The path takes the string team id; a slug 404s "Team not found in this organization". Org-owned repos only: a user's repo gets 400 "Only an organization’s repositories can be shared with teams", and listing a user's "org" teams 404s, so the provider explains it before the PUT. Missing repo → 404 "Repository not found". The team is `team_id` or `team_slug` (exactly one, slug matched case-insensitively and kept as configured); switching between them for the same team is a no-op, a different team forces a new resource. Access survives a repository rename (old slugs redirect), so `repository` renames are in place. Replacing the repository drops its team access: use `replace_triggered_by`. Import `owner/repo:teamId` or `owner/repo:team_slug` |
| 13 | ✅ | `ghost_team_membership` | resource | native | `PUT`/`DELETE /api/organizations/{org}/teams/{team}/members/{user}` and `…/maintainers/{user}` (all 204, no body). `{team}` must be the **slug**: the team id 404s "Team not found", so the provider stores the id and lists the org's teams for the current slug. **The user must already be a member of the organization**: anyone else gets 400 "<user> is not a member here", and org invitations are 🚫 with a PAT (#31), so the error says to invite them in the web UI. `role` = `member` (default) / `maintainer`. Adding a member is idempotent and does **not** dismiss a maintainer, so the provider reads the team first and appoints or dismisses explicitly. Both DELETEs return 204 when there's nothing to remove; removing a member also drops maintainer. Unknown user → 404 "User X not found". Read from `GET …/teams/{slug}` `members[]` (`username`, `maintainer`). Org, team slug and username are case-insensitive (kept as configured; case-only changes are in place). `team_id` or `team_slug`, exactly one, as in #12. Import `org:team:username` (team = id or slug). Acceptance tests use the token user on the org's existing team and restore its membership afterwards; changing the team is untested live (one team, and teams can't be created with a PAT) |
| 14 | ✅ | `ghost_organization` | data source | native | v3 has no `GET /orgs/{org}` (404 "Cannot GET"; only `/orgs/{org}/repos`), so native `GET /api/organizations/{slug}`, which is public (it answers without a token). Returns `slug`, `name`, `logo`, `createdAt`, `description`, `website`, `location`, `email`, `repositoryCount` (**public repositories only**, even for members), `members[]` (`username`, `name`, `image`, `public`; no roles), `pinned[]` (`slug`, `name`, `description`, `visibility`, in pinned order, only repos the viewer can read) and `viewerRole` (`member`/`admin`/`owner`, null outside the org). Members see every member; anyone else sees only public members. The slug is matched case-insensitively; `id` is Ghost's lower-case slug. Unlike the teams endpoints, an org the token user isn't in (e.g. `ghost`) reads fine; an unknown org or a user login is 404 "Organization not found: X". Members are sorted by username; `pinned_repositories` adds `full_name`. Ghost stores an empty or blank detail verbatim (`""` reads back as `""`, not null; `null` clears it). Deleting a pinned repository unpins it. Settings-only fields (`basePermission` etc.) are left to #15 |
| 15 | ✅ | `ghost_organization_settings` | resource | native | Singleton, admins only. `GET`/`PATCH /api/organizations/{slug}/settings` (`basePermission`, `membersCanCreatePublic/PrivateRepositories`, `allowPrivateForks`, `oauthAppRestrictions`, `defaultBranch`, `description`, `website`, `location`, `email`); every field accepts a PAT. The PATCH is partial and returns the full settings; `null` clears a field, and **an empty `{}` or an unknown field is a 500**, so the provider sends only changed fields and reads instead of sending nothing. Every argument is Optional+Computed: one left out is unmanaged (left as it is). `base_permission = "none"` and `""` for a string stand for Ghost's null (null reads back as `none`/`""`; a blank string Ghost stored is kept). Validation (400, `message` an array): `basePermission` is `read|triage|write|maintain|admin` (case-sensitive), description ≤ 350, location ≤ 100, `defaultBranch` ≤ 255 (checked at plan time); `website` must be a URL with a scheme (`ftp://` passes, `""` doesn't) and `email` an address (`""` doesn't), left to Ghost. `defaultBranch` isn't checked as a branch name (`bad..name` is stored) and becomes the `default_branch` of new repositories. The org slug is case-insensitive (case-only changes are in place); a non-member org (`ghost`), unknown org or user login is 404 "Organization not found: X"; anonymous is 401. **Destroy only removes it from state**: an org always has settings, Ghost's defaults aren't documented, and a reset would wipe what the org had before Terraform. Display name and logo are Better Auth / #34. Import by org slug |
| 16 | ✅ | `ghost_organization_webhook` | resource | native | `GET`/`POST /api/organizations/{slug}/webhooks`, `PATCH`/`DELETE …/{id}`, `POST …/{id}/secret`, `GET …/{id}/deliveries`. Verified live to behave exactly like #8: the same 37 events, POST ignores `active` and sends a `ping`, an empty `{}` PATCH is a 500, events are de-duplicated in order, the same 400s, and a missing hook is 404 "Webhook not found". Shares `client.Webhooks` and `webhook_model.go` with #8. An org hook receives the events of every repository the org owns (a `label.created` and a `repository.edited` on an org repo were delivered, signed with the hook's secret), but not of its members' own repositories, and it doesn't show up in a repo's webhook list. Admins only. An unknown org, a user login and an org the token user isn't in (e.g. `ghost`) are all 404 "Organization not found: X", so Create lists the org's hooks first to turn that into a helpful error. The slug is case-insensitive: it's kept as configured, a case-only change is in place, and `id` uses it in lower case. Another org forces a new webhook. Import `org:webhook_id` |
| 17 | 🚫 | `ghost_user_ssh_key` | resource | — | **Not possible with a PAT (checked 2026-10-28): a key can be added but never removed.** v3 `GET /user/keys` (200) and `POST /user/keys` (a bad key gets 400 "That is not a usable public key", so the route takes a PAT) work, but every key has `id: null` and there is no v3 `GET`/`PATCH`/`DELETE /user/keys/{id}` (404 "Cannot DELETE /api/v3/user/keys/…"). Native `GET`/`POST /api/ssh-keys` and `DELETE /api/ssh-keys/{id}` all return 403 "Personal access tokens cannot use this route; sign in instead". GraphQL has no key mutations, and there is no public `/<user>.keys`. A resource could create keys it could never destroy, leaving a credential with push access on the account, so it isn't built. Add SSH keys in the web UI |
| 18 | ✅ | `ghost_user_ssh_signing_key` | resource | v3 | `POST`/`GET /user/ssh_signing_keys`, `GET`/`DELETE /{id}`, all with a PAT (verified 2026-10-28; native `/api/ssh-signing-keys` returns 403). No update route, so `title` and `key` force a new key. Ghost stores the key as `<type> <base64>` (comment and whitespace dropped) and trims the title, so the provider ignores comment-only changes and rejects untrimmed titles. Title max 100 (422); an empty title becomes the key comment or type. A key already on the account gives 422 `already_exists`, and the error names its ID for import. Ids are numeric; import by id. Verified live: an SSH-signed commit shows on the native commit `verification` with `keyId` = the key's SHA256 fingerprint, and the reason changes from "not linked to <author email>" to "no Ghost account has uploaded" once the key is deleted. `verified: true` also needs the author email to be a verified address of the account, which a PAT can't read |
| 19 | 🚫 | `ghost_user_gpg_key` | resource | — | **Not possible with a PAT (checked 2026-10-28): no route lets a token add or remove a GPG key.** Native `GET`/`POST /api/gpg-keys` and `DELETE /api/gpg-keys/{id}` all return 403 "Personal access tokens cannot use this route; sign in instead". v3 has no GPG routes: `GET`/`POST /api/v3/user/gpg_keys` and `GET`/`DELETE /api/v3/user/gpg_keys/{id}` are 404 "Cannot GET …", as are the `gpg-keys` spellings and `/users/{user}/gpg_keys`. GraphQL (`/api/graphql`) has no key mutations or types, and there is no public `/<user>.gpg`. The OpenAPI spec lists only the native routes. No key was added during the probes. Add GPG keys in the web UI |

## Tier 3: nice to have

| # | Status | Name | Kind | API | Notes |
| - | ------ | ---- | ---- | --- | ----- |
| 20 | ❌ | `ghost_oauth_app` | resource | native | User or org (`organization` is ForceNew). `POST /api/oauth-apps` or `/api/organizations/{slug}/oauth-apps`, then `GET`/`PATCH`/`DELETE /api/oauth-apps/{clientId}`. `clientSecret` is returned once. Import `clientId` |
| 21 | ❌ | `ghost_organization_oauth_app_access` | resource | native | `PUT /api/organizations/{slug}/oauth-app-access/{clientId} {state}`, `DELETE`. Only matters with `oauthAppRestrictions`. Import `org:clientId` |
| 22 | ❌ | `ghost_organization_pinned_repositories` | resource | native | `PUT /api/organizations/{slug}/pins` sets the whole ordered list (≤ 6). Read from the org profile. Delete = `PUT []`. Import by org slug |
| 23 | ❌ | `ghost_repositories` | data source | native | `GET /api/repositories/{username}`. Public and private repos |
| 24 | ❌ | `ghost_branches` | data source | native | `GET …/branches` returns names and `defaultBranch` |
| 25 | ❌ | `ghost_release` | resource | v3 | CRUD on `/repos/{o}/{r}/releases[/{id}]`. `tag_name` is RequiresReplace (PATCH changes return 422). Don't model `make_latest`, `generate_release_notes`, `discussion_category_name`. Delete keeps the tag. Import `owner/repo:id`. Low demand: with the GitHub provider it mostly tags a freshly bootstrapped module repo, which relies on file writes Ghost lacks |
| 26 | ❌ | `ghost_release` | data source | v3 | `/releases/latest`, `/releases/tags/{tag}`, `/releases/{id}` |
| 27 | ❌ | `ghost_gitignore_templates` | data source | v3 | `/gitignore/templates[/{name}]` |
| 28 | ❌ | `ghost_licenses` | data source | v3 | `/licenses[/{key}]` |
| 29 | ❌ | `ghost_repository_status_contexts` | data source | native | `GET …/status-contexts`, the 50 most recent; helps fill ruleset required checks |
| 30 | ❌ | `ghost_organization_members` | data source | native | From the public org profile. No roles, private members hidden |
| 31 | ❌ | `ghost_organization_invitation` | resource | native | Poor fit: native `POST /invitations` only; there's no list, and cancelling is Better Auth only. Create-only at best. Consider skipping until Ghost adds endpoints |

## Tier 4: later / niche

| # | Status | Name | Kind | API | Notes |
| - | ------ | ---- | ---- | --- | ----- |
| 32 | ❌ | `ghost_repository_subscription` | resource | native | `GET`/`PUT …/subscription {level}`. Per user; delete = `participating` |
| 33 | ❌ | `ghost_release_asset` | resource | mixed | Upload: `POST /api/uploads/repos/{o}/{r}/releases/{id}/assets` (raw bytes; a PAT works). Manage: v3 `/releases/assets/{id}`. ForceNew on content hash. Counts against storage quota |
| 34 | ❌ | `ghost_organization_logo` | resource | native | `PUT`/`DELETE /api/organizations/{slug}/logo` (raw PNG/JPEG) |
| 35 | ❌ | `ghost_organization_public_membership` | resource | native | `PUT`/`DELETE /public-members`, for yourself only |
| 36 | ❌ | `ghost_user_email` | resource | native | Poor fit: verification happens out of band |
| 37 | ❌ | `ghost_repository_rulesets` | data source | v3 | List rulesets |
| 38 | ❌ | `ghost_codeowners_errors` | data source | v3 | `/codeowners/errors` |
| 39 | ❌ | `ghost_storage_usage` | data source | native | `GET /api/storage/{owner}` |

## Not possible

| Status | Item | Why |
| ------ | ---- | --- |
| 🚫 | `ghost_organization` (create/rename/delete) | Better Auth only, not reachable with a PAT. Use the data source |
| 🚫 | `ghost_team` (create/rename/delete) | Better Auth only. Use the data source |
| 🚫 | `ghost_organization_membership` (roles, removal) | Better Auth only |
| 🚫 | `ghost_user_ssh_key` (#17) | Native `/api/ssh-keys` rejects PATs; v3 `/user/keys` can add a key but has no id and no delete |
| 🚫 | `ghost_user_gpg_key` (#19) | Native `/api/gpg-keys` rejects PATs, and v3 and GraphQL have no GPG key routes |
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
