# App template

Copy this folder when adding a new app.

Replace:

```text
NAMESPACE
APP_NAME
APP_HOST
```

Recommended first exposure: internal-only via `envoy-internal`.

If the app uses persistent data, run:

```sh
scripts/volsync-app-bootstrap.sh APP_NAME
```
