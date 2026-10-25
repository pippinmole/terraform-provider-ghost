# Terraform Provider for Ghost

A Terraform provider for [Ghost](https://web.ghost.procrastinator.fyi), a GitHub alternative.
Built on the [Terraform Plugin Framework](https://developer.hashicorp.com/terraform/plugin/framework).

See [ROADMAP.md](ROADMAP.md) for which resources are supported.

## Usage

```hcl
terraform {
  required_providers {
    ghost = {
      source = "pippinmole/ghost"
    }
  }
}

provider "ghost" {
  token = var.ghost_token # or GHOST_TOKEN
  # base_url = "https://api.ghost.procrastinator.fyi" # or GHOST_BASE_URL
}
```

## Requirements

- [Terraform](https://developer.hashicorp.com/terraform/downloads) >= 1.0
- [Go](https://go.dev/doc/install) (version in `go.mod`) to build the provider

## Developing

```sh
make install   # builds and installs the binary into $(go env GOPATH)/bin
make test      # unit tests
make testacc   # acceptance tests against live Ghost (needs GHOST_TOKEN, creates real resources)
make generate  # regenerates docs/ with tfplugindocs
make lint      # golangci-lint
```

### Trying the provider locally

`playground/` is a single config that uses every resource and data source the provider implements:

```sh
make install dev-tfrc
export TF_CLI_CONFIG_FILE="$PWD/dev.tfrc"
cp playground/terraform.tfvars.example playground/terraform.tfvars   # add your token; *.tfvars is gitignored
terraform -chdir=playground apply                                    # no `terraform init` needed with dev_overrides
terraform -chdir=playground destroy
```

### Layout

| Path | Purpose |
| ---- | ------- |
| `main.go` | Provider entrypoint |
| `internal/provider` | Provider, resources and data sources |
| `internal/client` | Hand-written typed client for the Ghost API (prefers `/api/v3`, falls back to native `/api`) |
| `examples/` | Example configs used by `tfplugindocs` |
| `docs/` | Generated documentation, don't edit by hand |
| `playground/` | Everything-example config for manual end-to-end testing; add each new resource here |

## Releasing

Push a `v*` tag. The release workflow runs GoReleaser and signs the checksums with the GPG key in the
`GPG_PRIVATE_KEY` / `PASSPHRASE` repository secrets. The public key must be registered with the
`pippinmole` namespace on the Terraform Registry.
