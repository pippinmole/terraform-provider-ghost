## 0.1.0 (Unreleased)

FEATURES:

* Initial provider scaffold with `token` and `base_url` configuration.
* **New Resource:** `ghost_repository`
* **New Resource:** `ghost_issue_label`
* **New Data Source:** `ghost_repository`
* **New Data Source:** `ghost_user`

ENHANCEMENTS:

* Requests that hit Ghost's rate limit (`429`) are retried once the limit resets, honouring `Retry-After` and `X-RateLimit-Reset`. When a response reports no requests left, the provider waits for the reset instead of sending more.
