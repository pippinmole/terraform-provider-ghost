# CLAUDE.md

Terraform provider for [Ghost](https://web.ghost.procrastinator.fyi), a GitHub alternative. It uses the Terraform Plugin Framework, protocol 6. API reference: https://api.ghost.procrastinator.fyi/reference

## Commits

Use [Conventional Commits](https://www.conventionalcommits.org/): `type(scope): summary`. Write the summary in lower case and the imperative mood, with no trailing full stop.

```
fix(provider): plan with repository causes 404
feat(provider): add ghost_branch resource
fix(client): escape owner in repository paths
docs(roadmap): mark ghost_repository done
chore(deps): bump terraform-plugin-framework to v1.20
```

- **Types:** `feat`, `fix`, `docs`, `test`, `refactor`, `perf`, `build`, `ci`, `chore`, `revert`.
- **Scopes:** `provider` (resources, data sources, provider config), `client` (`internal/client`), `docs` (generated docs, README), `roadmap`, `changelog`, `examples`, `playground`, `ci`, `deps`, `release`. Leave the scope out when nothing fits.
- **Breaking changes:** add `!` after the type or scope (`feat(provider)!: …`) and include a `BREAKING CHANGE:` footer.
- **Body:** explain *why*, wrapped at 72 columns.

## Branches

Name branches `type/short-kebab-description`, using the same types as commits:

```
feat/ghost-repository
fix/repository-owner-case-404
docs/playground-readme
chore/bump-plugin-framework
```

## Working in this repo

- **Build and test:** `make build`, `go test ./...`. Run `go generate ./...` after any schema or description change, and commit the regenerated `docs/`. Never edit `docs/` by hand.
- **Acceptance tests** run against live Ghost and create real repositories: `GHOST_TOKEN=… TF_ACC=1 go test ./internal/provider/ -run TestAcc -timeout 20m`. Set `GHOST_TEST_ORGANIZATION` to an organization with at least one team to also run the organization tests. Name test repositories `tf-acc-*`, and always clean them up.
- **Auth:** a personal access token is sent as `Authorization: Bearer …`. Both `/api/v3` and the native `/api` accept it. Prefer v3, and fall back to native where v3 doesn't cover an operation. Never add cookie or session-based auth.
- **Playground:** `playground/main.tf` is the everything-example, a single config that uses every resource and data source. Whenever you add a resource or data source, add it there in the same change, covering every argument and lookup form. Users copy `playground/terraform.tfvars.example` to `terraform.tfvars`.
- **Secrets:** `*.tfvars` is gitignored and holds the real token. Never commit, print or log a token.
- **Tracking:** update `ROADMAP.md` (✅ 🚧 ❌ 🚫) and the `Unreleased` section of `CHANGELOG.md` with each feature.
