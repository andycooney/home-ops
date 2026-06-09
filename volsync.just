set shell := ['bash', '-euo', 'pipefail', '-c']
set script-interpreter := ['bash', '-euo', 'pipefail']

[doc('Bootstrap app-scoped VolSync/Kopia repository and 1Password fields')]
bootstrap app:
    ./scripts/volsync-app-bootstrap.sh {{app}}

[doc('Migrate one app standard VolSync PVC by selecting/restoring a Kopia snapshot')]
migrate app namespace="default":
    scripts/volsync-pvc-migrate-app.sh {{app}} {{namespace}}

[doc('Update Git so an app is resumed after VolSync PVC migration')]
resume app:
    scripts/volsync-pvc-resume-app-git.sh {{app}}
