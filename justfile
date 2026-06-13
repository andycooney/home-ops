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
[group('kube')]
sanity-check:
    just kube sanity-check

[doc('Show a concise Flux/Kubernetes health overview')]
[group('kube')]
flux-overview:
    just kube flux-overview

[group: 'volsync']
mod? volsync 'volsync.just'
