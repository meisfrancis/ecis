# ecis — an ECS interactive shell

`ecis` is a terminal UI for Amazon ECS in the spirit of [k9s](https://k9scli.io):
the same `:` command prompt, `/` filter, `Esc` to go back, vim navigation and
`Shift-<key>` sorting, applied to clusters, services, tasks and containers
instead of Kubernetes resources.

On top of the k9s feature set it adds two things ECS operators usually leave the
terminal for:

- **Live CPU and memory monitoring** — utilization columns in every table, plus
  braille line charts drawn from CloudWatch for a cluster, a service or a single
  task.
- **Cost** — spend from AWS Cost Explorer, broken down by ECS service, cluster,
  usage type or AWS service, with a month-end forecast and a local Fargate
  estimator for accounts that have never activated cost allocation tags.

```
 ___   ___  ___  ___     Profile:  prod              <enter> Drill down   <:cmd>  Command
| __| / __||_ _|/ __|    Region:   eu-west-1         <l>     Logs         </>     Filter
| _| | (__  | | \__ \    Account:  1234… (deploy)    <s>     Scale        <?>     Help
|___| \___||___||___/    Cluster:  prod              <r>     Restart      <esc>   Back
                         Workload: 42 running, 0 pending, 11 services
 ecs interactive shell   CPU:      ███████░░░░░░░ 48.2%
 v0.1.0                  MEM:      █████████░░░░░ 63.7%
                         Cost:     $1284.40 mtd
```

## Install

```sh
go install github.com/meisfrancis/ecis/cmd/ecis@latest
```

Or from a clone:

```sh
make build      # ./bin/ecis
make install    # $GOBIN/ecis
```

Go 1.24 or later.

## Quick start

```sh
ecis                                  # open on the cluster list
ecis --cluster prod                   # open on the services of a cluster
ecis --profile staging --region eu-west-1
ecis -c pulse                         # open on the dashboard
ecis -c cost                          # open on the spend view
ecis --readonly                       # disable every mutating action
```

Press `?` for the keyboard reference, `:` for the command prompt and `Ctrl-A`
for the alias list.

## Concepts

The one place `ecis` deliberately departs from k9s is the notion of scope. k9s
scopes views to a **namespace**; `ecis` scopes them to a **cluster**. Selecting a
cluster (`Enter` on the cluster list, or `:cluster prod`) is what makes
`:services`, `:tasks`, `:events` and the rest resolve.

Everything else maps across:

| k9s | ecis |
| --- | --- |
| context (kubeconfig) | AWS profile — `:ctx` |
| namespace | ECS cluster — `:cluster <name>` |
| deployment | ECS service — `:svc` |
| pod | ECS task — `:tasks` (aliased `:po`) |
| container | container — `Enter` on a task |
| node | container instance — `:instances` (aliased `:nodes`) |
| `kubectl exec` | ECS Exec — `s` on a task |
| pod logs | CloudWatch Logs — `l` |
| `describe` / `yaml` | `d` / `y` |
| pulse | `:pulse` |

## Commands

Type `:` then a command or any of its aliases. `Ctrl-A` shows this list in-app.

| Command | Aliases | What it shows |
| --- | --- | --- |
| `:clusters` | `cl`, `cluster`, `ns` | ECS clusters in the region |
| `:services` | `svc`, `service`, `deploy` | Services in the active cluster |
| `:tasks` | `ts`, `task`, `po`, `pods` | Tasks (running and stopped) |
| `:running` | `run` | Running tasks only |
| `:stopped` | `stop`, `failed` | Stopped tasks, newest first |
| `:taskdefs` | `td`, `taskdef`, `jobs` | Task definitions and revisions |
| `:instances` | `ci`, `node`, `nodes`, `ec2` | Container instances (EC2 capacity) |
| `:capacity` | `cp`, `providers` | Capacity providers |
| `:events` | `ev`, `event` | Service events across the cluster |
| `:deployments <svc>` | `dp`, `rollout` | Deployments of a service |
| `:metrics [svc]` | `mon`, `top`, `cpu`, `mem` | CPU and memory charts |
| `:cost` | `$`, `spend`, `billing` | Cost Explorer spend |
| `:pulse` | `pu`, `dash` | Fleet dashboard |
| `:contexts` | `ctx`, `profile` | Switch AWS profile |
| `:regions` | `region` | Switch AWS region |
| `:aliases` | `alias`, `a` | This table |
| `:help` | `h`, `?` | Keyboard reference |
| `:q` | `quit`, `exit` | Quit |

Some commands take an argument: `:cluster prod`, `:svc prod`, `:tasks checkout`,
`:metrics checkout`, `:deployments checkout`, `:ctx staging`, `:region us-east-1`.

## Keys

### Global

| Key | Action |
| --- | --- |
| `:` | Command prompt (with completion) |
| `/` | Filter the current view |
| `?` | Help |
| `Esc` | Clear the filter, or go back one view |
| `Ctrl-A` | Alias list |
| `Ctrl-R` | Force a reload |
| `Ctrl-E` | Show/hide the header |
| `Ctrl-C`, `:q` | Quit |

Filters accept a substring or a regular expression, and `!` inverts them:
`web`, `^checkout-`, `!stopped`.

### Navigation and tables

| Key | Action |
| --- | --- |
| `j` / `k`, `↓` / `↑` | Move down / up |
| `g` / `G` | First / last row |
| `Ctrl-F` / `Ctrl-B` | Page down / up |
| `Enter` | Drill into the selected row |
| `Space` | Mark a row and advance |
| `Ctrl-\` | Clear all marks |
| `Ctrl-W` | Toggle wide columns |
| `Shift-N` `Shift-C` `Shift-M` `Shift-A` `Shift-S` | Sort by name / CPU / memory / age / status |

Press a sort key twice to reverse it. Values that cannot be sorted (`-`, `n/a`)
always sink to the bottom. Actions apply to every marked row, falling back to the
selected row when nothing is marked — so `Space Space Ctrl-D` stops two tasks.

### Services

| Key | Action |
| --- | --- |
| `Enter` | Tasks of the service |
| `s` | Scale (prompts for a desired count) |
| `r` | Restart — force a new deployment |
| `l` | Tail logs from every task of the service |
| `m` | CPU and memory charts |
| `e` | Service events |
| `p` | Deployments |
| `$` | Cost breakdown |
| `d` / `y` | Describe / YAML |

### Tasks and containers

| Key | Action |
| --- | --- |
| `Enter` | Containers of the task |
| `l` | Tail logs |
| `s` | Shell in via ECS Exec |
| `m` | Per-task charts |
| `Ctrl-D` | Stop the marked tasks |
| `d` / `y` | Describe / YAML |

### Logs

| Key | Action |
| --- | --- |
| `s` | Toggle autoscroll |
| `w` | Toggle wrapping |
| `t` | Toggle timestamps |
| `f` | Toggle fullscreen |
| `c` | Clear the buffer |
| `0` `1` `2` `3` `4` `5` | Show all / 1m / 5m / 15m / 1h / 24h |
| `/` | Filter lines |
| `Ctrl-S` | Save to a file |

### Charts

| Key | Action |
| --- | --- |
| `1` `2` `3` `4` `5` | Window: 15m / 1h / 6h / 24h / 7d |
| `Ctrl-R` | Reload from CloudWatch |

### Cost

| Key | Action |
| --- | --- |
| `b` | Cycle the breakdown |
| `n` | Toggle daily / monthly granularity |
| `w` | Toggle ECS-only / all container spend |
| `e` | Toggle the local Fargate estimate |
| `Enter` | Full history for the selected group |

## Monitoring

CPU and memory come from CloudWatch, and how much detail is available depends on
what the cluster publishes:

| Level | Source | Requires |
| --- | --- | --- |
| Service | `AWS/ECS` `CPUUtilization` / `MemoryUtilization` | nothing — always published |
| Cluster | `ECS/ContainerInsights` utilized vs reserved, falling back to `AWS/ECS` | Container Insights, or EC2 capacity |
| Task | `ECS/ContainerInsights` `TaskCpuUtilization` / `TaskMemoryUtilization` | Container Insights with **enhanced observability** |

Where a metric is not published the column shows `-` and the chart explains why
rather than drawing an empty pane. Nothing fails: a Fargate-only cluster without
Container Insights still gets per-service utilization, which is the number that
matters most.

Enable Container Insights on a cluster with:

```sh
aws ecs update-cluster-settings --cluster prod \
  --settings name=containerInsights,value=enhanced
```

Charts are drawn with braille glyphs (2×4 sub-cell resolution), so a 60-point
series stays readable in a handful of terminal rows. The header carries
cluster-wide CPU and memory gauges wherever you are in the app.

## Cost

The `:cost` view queries **AWS Cost Explorer**. Point it at a breakdown with `b`:

- **ECS service** and **cluster** — need the `ecs:service-name` and
  `ecs:cluster-name` cost allocation tags activated in Billing → Cost allocation
  tags. Activation is not retroactive, so figures start from the day you enable
  them.
- **Usage type** — Fargate vCPU-hours versus GB-hours; needs no tags.
- **AWS service**, **region**, **account** — no tags needed.

`w` widens the scope from Amazon ECS alone to the whole container stack
(EC2 capacity and ECR storage), which is what you want for EC2-launch-type
clusters: Fargate bills under ECS, but EC2 capacity bills under EC2 no matter how
many tasks are packed onto it.

### The estimator

When Cost Explorer returns nothing for a tag-based breakdown, `ecis` falls back
to a local projection computed from the reserved size of the running Fargate
tasks, and says so. You can also force it with `e`.

The estimate is a modelled figure, not billed spend. It uses a built-in table of
indicative on-demand Linux/x86 Fargate rates and assumes 730 hours a month; it
does not know about Spot, Savings Plans, Graviton, Windows, or ephemeral storage
above the included 20 GB. Override the rates when precision matters:

```yaml
cost:
  rates:
    eu-west-1:
      vcpuHour: 0.04456
      gbHour: 0.004865
```

### Cost Explorer charges per request

Every Cost Explorer API call is billed (about $0.01 at the time of writing), so
`ecis` is deliberately frugal:

- The `:cost` view refreshes every 15 minutes, not on the table refresh rate.
- The header's month-to-date figure refreshes hourly.
- The pulse dashboard reuses the cost view's cached result.
- `Ctrl-R` forces a reload when you want one now.

Tune it with `cost.refreshMinutes`, or drop the header figure entirely by
denying `ce:GetCostAndUsage` — the field simply reads `n/a`.

## IAM

Read-only browsing, monitoring and cost:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "ecs:ListClusters", "ecs:DescribeClusters",
        "ecs:ListServices", "ecs:DescribeServices",
        "ecs:ListTasks", "ecs:DescribeTasks",
        "ecs:ListTaskDefinitions", "ecs:ListTaskDefinitionFamilies",
        "ecs:DescribeTaskDefinition",
        "ecs:ListContainerInstances", "ecs:DescribeContainerInstances",
        "ecs:DescribeCapacityProviders",
        "cloudwatch:GetMetricData",
        "logs:FilterLogEvents", "logs:DescribeLogGroups",
        "ce:GetCostAndUsage", "ce:GetCostForecast",
        "ec2:DescribeInstances",
        "sts:GetCallerIdentity"
      ],
      "Resource": "*"
    }
  ]
}
```

Add these for the mutating actions (`s`, `r`, `Ctrl-D`, `s` to exec):

```
ecs:UpdateService
ecs:StopTask
ecs:ExecuteCommand
ssmmessages:CreateControlChannel
ssmmessages:CreateDataChannel
ssmmessages:OpenControlChannel
ssmmessages:OpenDataChannel
```

Run with `--readonly` (or `readOnly: true`) to disable them in the UI regardless
of what the credentials permit.

### ECS Exec

`s` on a task opens an interactive session. Like k9s shelling out to `kubectl`,
`ecis` shells out to the AWS CLI, so it needs:

- the `aws` CLI and the [session-manager-plugin](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-working-with-install-plugin.html) on `PATH`,
- the task started with `enableExecuteCommand` (the `EXEC` column shows this in
  wide mode),
- a task role with the `ssmmessages` permissions above.

## Configuration

`ecis` runs with no configuration. To change the defaults, create
`~/.config/ecis/config.yml` (or set `ECIS_CONFIG_DIR`):

```yaml
refreshRate: 5          # table reload interval, seconds
maxLogLines: 5000       # in-memory log buffer per view
logSinceSeconds: 300    # how much log history to load on open
readOnly: false         # disable scale, restart, stop and exec
profile: ""             # default AWS profile
region: ""              # default AWS region
cluster: ""             # cluster to open on

metrics:
  periodSeconds: 60     # CloudWatch aggregation period
  windowMinutes: 60     # default chart window
  containerInsights: true

cost:
  clusterTagKey: "ecs:cluster-name"
  serviceTagKey: "ecs:service-name"
  lookbackDays: 30
  estimate: true        # fall back to the local Fargate estimate
  includeCredits: false # fold credits and refunds into totals
  refreshMinutes: 15
  rates: {}             # per-region Fargate price overrides
```

Saved describes, YAML dumps and log exports land in `~/.config/ecis/dumps/`.

## Development

```sh
make build     # compile to ./bin/ecis
make test      # go test ./...
make race      # go test -race ./...
make lint      # gofmt check and go vet
make check     # everything
```

The layout:

```
cmd/ecis          entry point, flags, startup
internal/awsx     AWS: ECS, CloudWatch, Cost Explorer, Logs, pricing, exec
internal/model    domain types the UI renders
internal/ui       widgets: app shell, table, charts, logs, details, prompt
internal/view     resource views and the command registry
```

Views are built on one generic `Browser`, which owns the refresh loop, sorting,
filtering, marking and drill-down; a new resource view is a set of columns, a
loader and a key handler.

## Limitations

- Cost Explorer figures lag real usage by up to 24 hours, as they do in the
  console. The estimator is a projection, not a bill.
- The YAML view (`y`) renders the AWS API shape with unset fields omitted, so a
  describe stays readable instead of filling the screen with nulls. Values that
  carry meaning — `false`, `0` — are kept.
- Per-task utilization needs Container Insights with enhanced observability.
- Only the `awslogs` driver can be tailed. Containers on FireLens or another
  driver are listed, with an explanation instead of an empty log pane.
- Task definitions are listed at their latest active revision by default; drill
  into a family for its full revision history.

## License

MIT — see [LICENSE](LICENSE).
