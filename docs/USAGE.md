# rvis user guide

This guide covers everything rvis can do. For a short overview and installation steps, see the [README](../README.md).

## Usage

```sh
# A local chart with your values (the flags are passed to `helm template`)
helm rvis ./mychart -f values-prod.yaml --set api.replicas=6

# A chart from a repository, with the meters sized to a 16 core / 64Gi node
helm rvis bitnami/redis --cpu 16 --memory 64Gi

# An installed release (`helm get manifest`)
helm rvis --release my-app -n production

# Pre-rendered manifests from a file or a pipe
helm rvis --manifest rendered.yaml
helm template my-app ./mychart | helm rvis

# CI: print a table and fail on misconfigured containers
helm rvis ./mychart -o table --fail-on error
```

### Live reload

While the interactive view is open, rvis watches the chart directory (including subcharts in `charts/`), the `-f` values files, `--set-file` files and `--manifest` files. When you save a change it re-renders the chart and updates the view in place:

- The `Watch:` line in the header shows `● live`, when the last reload happened, and how much the total CPU and memory requests changed (e.g. `cpu +440m mem +256Mi`).
- Rows that changed in the last reload are marked with a yellow `●` for a few seconds.
- The selection, sort, filter and any open detail panel are kept across reloads.
- If the chart fails to render (for example, a template is half-edited), a red banner shows Helm's error and the view keeps showing the last good render. `F4` shows the full error. The banner clears on the next successful render.

Watching is on by default. Use `--watch=false` to turn it off or `--watch-interval` to change how often files are checked (default `500ms`). Releases, stdin and remote charts are not watched.

When stdout is a terminal, rvis opens the interactive view. Otherwise it prints the same view as a static table. Use `-o json` to get the full report for scripts.

### Editing requests and limits

Press `e` (or `F7`) on a workload or container to edit its requests and limits in place. A form opens with every container's CPU and memory requests and limits. Move between cells with `Tab` and the arrow keys, type a quantity (`250m`, `1.5`, `512Mi`, `2Gi`) and press `Enter` to write all changes to the chart. `Esc` cancels, `Ctrl+U` clears a cell and `Ctrl+Z` restores its original value.

For the focused cell, the form shows where the value is defined, for example `mychart/values.yaml:10` with key `api.resources.requests.cpu`. rvis finds this by tracing rather than guessing from key names:

1. It collects every values entry and template literal whose value matches the rendered one.
2. It renders a temporary copy of the chart in which each candidate holds a unique marker value.
3. Whichever marker reaches the container's field is the source.

This handles the following cases:

- **`-f` override files:** the value is written to the file that actually sets it. If both `values.yaml` and `-f prod.yaml` define it, `prod.yaml` is edited.
- **Literal values in templates:** the literal in the template file is edited.
- **Shared values:** when one value feeds several containers or workloads (for example, a shared `resources` block), the form warns `shared value, also sets: …` before you save.
- **Missing fields:** a value that isn't set yet (for example, a CPU limit next to an existing memory limit, or a whole `limits` block next to `requests`) is added as a new key beside its sibling. rvis checks the new key with another marker render first.
- **Not editable:** values computed in templates or set with `--set` are marked as not editable, with the reason.

Edits change only the value text, so comments, quoting and layout are preserved. Before saving, the form checks that every value is a valid quantity and that no request exceeds its limit. With live reload on, the view updates as soon as the file is written.

Editing requires a local chart directory. It is not available for installed releases, stdin, rendered manifest files or repository charts.

### Editing replicas

The replica count is editable in both edit modes. In quick edit it is the PODS cell of each workload row. In the `e` form it is the REPLICAS row above the containers. The value you edit is the one that decides the pod count:

- **Deployments, StatefulSets and other replicated workloads:** `spec.replicas`.
- **Jobs and CronJobs:** `parallelism`.
- **Autoscaled workloads:** the HorizontalPodAutoscaler's `minReplicas` when it is at or above `spec.replicas`, since the minimum is what sets the floor. The label says which one you are editing, e.g. `autoscaler minReplicas (max 12)`. A minimum above the autoscaler's maximum, or below 1, is rejected.
- **Not editable:** DaemonSets (one pod per node; use `--nodes` to model the cluster size) and bare Pods.

Replica counts are traced like resources, but with small integer markers, because replica counts must stay integers to render and charts often loop over them. Other values that happen to hold the same number, such as `cpu: 3` next to `replicaCount: 3`, are not mistaken for the count. When a value comes from the defaults of a packaged subchart (`charts/*.tgz`), the form says so and names the key to override it under in your values file.

### Quick edit: the whole table at once

Press `E` (or `F8`) to make the whole table editable. The cursor moves between the CPU and memory request and limit cells of every container, and you can type new values anywhere. As you type:

- Workload rows, the TOTAL row and the header meters are recomputed from the typed values with the same rules as the normal view (init containers, sidecars, replicas, autoscaling minimums).
- An `Editing:` line in the header shows how many changes are pending and how they move the totals, e.g. `cpu −2.6/+2  mem ±0/±0 req/lim`.
- Rows with pending changes are marked `✎`, and rows stay in place instead of re-sorting under the cursor.
- The status line shows where the focused value will be written (`values.stg.yaml:113 worker.gunicorn.resources.limits.cpu`), or why it can't be. Sources are located in the background, starting with the focused workload.
- Container cells show the values as the chart writes them. A request that is not set but is defaulted from its limit is shown dimmed with `*`.

Keys: `Tab`/`Shift+Tab` move across a row and wrap to the next one, the arrow keys move freely, and `Enter` moves down. `Ctrl+U` clears a cell, and `Ctrl+Z` restores its saved value. Typing the saved value back also drops the change.

`Ctrl+S` checks every value (valid quantities, no request above its limit) and moves the cursor to the first problem, if there is one. If everything is valid, it writes all changes in one pass. Every workload is located against the files as they are before anything is written, so:

- a value shared by several workloads is written once,
- conflicting values for one shared source are rejected,
- nothing is written if any change fails.

`Esc` leaves quick edit; with pending changes it asks once before discarding them.

### Keys

| Key | Action |
| --- | --- |
| `↑` `↓` / `j` `k`, `PgUp` `PgDn`, `g` `G` | Move the selection |
| `Enter` | Show the detail panel for the selected workload: containers, roles, effective pod, × replicas, HPA max, extended resources, images and findings |
| `F2` / `r` | Toggle between the total footprint (pods × replicas) and a single pod |
| `F3` / `/` | Filter by workload, container or image |
| `F4` / `w` | List all findings |
| `F5` / `t` | Show or hide containers under each workload |
| `F6` / `s`, `>` `<` | Move to the next or previous sort column. `I` inverts the order |
| `c` `C` `m` `M` `n` `p` | Sort by CPU request/limit, memory request/limit, name or pods (press again to invert) |
| `F8` / `E` | Quick edit: make every request, limit and replica count in the table editable, with totals updating as you type; `Ctrl+S` saves everything |
| `F7` / `e` | Edit the requests and limits of the selected workload and write them to the chart |
| `F1` / `?` | Help and legend |
| `F10` / `q` | Quit |

The mouse wheel scrolls, and clicking a row selects it.

### Flags

| Flag | Description |
| --- | --- |
| `-f, --values`, `--set`, `--set-string`, `--set-file`, `--set-json` | Values passed to `helm template` |
| `--version`, `--repo`, `--devel`, `--kube-version`, `-a, --api-versions` | Chart resolution and capabilities passed to `helm template` |
| `--release NAME` | Inspect an installed release instead of rendering a chart |
| `--manifest FILE` | Read rendered manifests from a file (`-` for stdin) |
| `-n, --namespace`, `--kube-context`, `--kubeconfig` | Cluster selection (defaults come from Helm's environment) |
| `-o, --output` | `auto` (default), `tui`, `table` or `json` |
| `--sort` | `cpu-req` (default), `cpu-lim`, `mem-req`, `mem-lim`, `pods`, `name`, `kind`, `manifest` |
| `--flat` | Show workloads only |
| `--per-pod` | Show one pod per workload instead of pods × replicas |
| `--cpu`, `--memory` | Capacity used to scale the header meters, e.g. a node size |
| `--nodes N` | Number of nodes running each DaemonSet (default 1) |
| `--fail-on` | `none` (default), `warn` or `error`: exit with code 2 when findings at that level exist |
| `--watch`, `--watch-interval` | Live reload in the interactive view (default on, checked every `500ms`) |
| `--width`, `--color` | Width of the static table, and `auto`/`always`/`never` color |

`NAME CHART` (as in `helm template`) is accepted as well as `CHART` alone.

## How the numbers are computed

rvis applies the same rules as the Kubernetes scheduler:

- **Effective pod resources:** the larger of the sum of the app containers plus [native sidecars](https://kubernetes.io/docs/concepts/workloads/pods/sidecar-containers/) (init containers with `restartPolicy: Always`), and each init container plus the sidecars started before it. A heavy init container can therefore set the pod's request, as `rvis-api` shows above.
- **Requests default to limits:** a container that sets only a limit gets a request equal to that limit. The detail panel marks these values with `*`.
- **Replicas:** `spec.replicas` (default 1), raised to an HPA's `minReplicas` when one targets the workload (`↑`). The header also shows the totals at the HPA's `maxReplicas`. Jobs and CronJobs use `parallelism`. DaemonSets count `--nodes` pods.
- **Memory units** are shown as the chart wrote them. `3G` (decimal, 3,000,000,000 bytes) stays `3G`, and `3Gi` (binary, 3 × 1024³ bytes) stays `3Gi`. A total that mixes both is shown in binary units.
- **QoS class:** Guaranteed, Burstable or BestEffort, following the Kubernetes rules.
- **Missing values** stay visible instead of being counted as zero: `—` means no request is set, `∞` means no limit is set, and `1.5+` means only some containers set the value. In the bars, `»` marks an unbounded limit.

Supported workloads: Deployment, StatefulSet, DaemonSet, ReplicaSet, ReplicationController, Job, CronJob and Pod. CRDs with a pod template at `spec.template` (Argo Rollouts and others) are also supported, as are `List` objects. Extended resources such as `nvidia.com/gpu` and `ephemeral-storage` are included.

### Findings

| Severity | Finding |
| --- | --- |
| error | The limit is lower than the request |
| warn | No memory limit, no CPU request, or no memory request |
| info | No CPU limit (often intentional), or memory set in decimal units (`3G` = 3,000,000,000 bytes ≈ `2.8Gi`) |
