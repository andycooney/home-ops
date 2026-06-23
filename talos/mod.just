set quiet
set shell := ['bash', '-euo', 'pipefail', '-c']
set script-interpreter := ['bash', '-euo', 'pipefail']

[private]
default:
    just -l talos

[doc('Render Talos configuration for one node')]
[group('talos')]
[script]
render-config node:
    source_dir="{{ source_directory() }}"
    common="$(mktemp)"
    patch="$(mktemp)"
    trap 'rm -f "$common" "$patch"' EXIT

    minijinja-cli --env "${source_dir}/machineconfig.yaml.j2" | vals eval -f - > "$common"
    minijinja-cli --env "${source_dir}/nodes/{{ node }}.yaml.j2" | vals eval -f - > "$patch"

    talosctl machineconfig patch "$common" -p "@${patch}"

[doc('Generate Talos configuration')]
[group('talos')]
[script]
generate-config node='all':
    source_dir="{{ source_directory() }}"
    out_dir="${source_dir}/clusterconfig"
    mkdir -p "$out_dir"

    common="$(mktemp)"
    trap 'rm -f "$common" "${patch:-}"' EXIT

    minijinja-cli --env "${source_dir}/machineconfig.yaml.j2" | vals eval -f - > "$common"

    requested="{{ node }}"
    if [[ "$requested" == "all" || "$requested" == "*" ]]; then
        mapfile -t nodes < <(find "${source_dir}/nodes" -maxdepth 1 -name 'talos*.yaml.j2' -exec basename {} .yaml.j2 \; | sort)
    else
        case "$requested" in
            talos*) nodes=("$requested") ;;
            *) nodes=("$(printf 'talos%02d' "$requested")") ;;
        esac
    fi

    for node in "${nodes[@]}"; do
        patch="$(mktemp)"
        minijinja-cli --env "${source_dir}/nodes/${node}.yaml.j2" | vals eval -f - > "$patch"

        out="${out_dir}/kubernetes-${node}.yaml"
        talosctl machineconfig patch "$common" -p "@${patch}" > "$out"

        rel="${out#${source_dir}/}"
        echo "wrote $rel"
        echo "validating $rel"
        talosctl validate --config "$out" --mode metal

        rm -f "$patch"
    done

