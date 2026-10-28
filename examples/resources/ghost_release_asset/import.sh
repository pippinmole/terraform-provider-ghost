# Release assets are imported by the repository's full name and the numeric
# asset ID, "owner/slug:asset_id". The import downloads the asset once to work
# out content_sha256. Set content_type in the configuration to the imported
# asset's type, or the default (application/octet-stream) replaces it.
terraform import ghost_release_asset.linux my-user/example:456
