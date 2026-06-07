set quiet
set shell := ['bash', '-euo', 'pipefail', '-c']
set script-interpreter := ['bash', '-euo', 'pipefail']

[group: 'bootstrap']
mod? bootstrap 'bootstrap'

[group: 'kubernetes']
mod? kube 'kubernetes'

[group: 'talos']
mod? talos 'talos'

[private]
default:
    just -l

[private]
log lvl msg *args:
    gum log -t rfc3339 -s -l "{{ lvl }}" "{{ msg }}" {{ args }}

[doc('Run a read-only post-change sanity check')]
[group('kubernetes')]
sanity-check:
    ./scripts/sanity-check.sh

[group: 'template']
mod? template 'template'

[doc('Render and validate configuration files')]
[group('template')]
configure:
    just template configure

[doc('Initialize configuration files (cluster.toml, age key, deploy key, push token)')]
[group('template')]
init:
    just template init

# Migrate one app's standard VolSync PVC by selecting/restoring a Kopia snapshot.
volsync-migrate app namespace="default":
    scripts/volsync-pvc-migrate-app.sh {{app}} {{namespace}}

# Update Git so an app is resumed after VolSync PVC migration.
volsync-resume app:
    scripts/volsync-pvc-resume-app-git.sh {{app}}
