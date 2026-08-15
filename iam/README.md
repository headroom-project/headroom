# AWS access

headroom works without any AWS access at all. A plan file already contains the
topology, the declared sizes, and every data source result, because data sources
are resolved at plan time. Everything the rules do today runs on that alone.

Access buys exactly two things the plan cannot state:

1. **The real ceiling instead of the documented default.** A custom parameter
   group can move `max_connections`. An account can have its Lambda concurrency
   quota raised from 1000. Today those findings carry lowered confidence; with
   read access they become facts.
2. **Consumption instead of capacity.** A subnet's `AvailableIpAddressCount`,
   a burstable instance's `CPUCreditBalance`, the depth of a queue. Declared
   capacity says what could happen; these say what is happening.

It does **not** buy discovery. Resources nobody put in Terraform stay invisible
either way until inventory is a separate feature.

## Two modes

**Mode A, collector in your account (default).** The CLI runs where your code
already runs, in CI or on a workstation, using credentials you already have.
Nothing is granted to anyone outside your account. Attach
`headroom-readonly-policy.json` to whatever principal runs it.

**Mode B, cross-account role (continuous analysis).** Create a role with
`headroom-readonly-policy.json` attached and `headroom-trust-policy.json` as its
trust relationship. The external id is issued per organization and is what stops
a confused deputy: without it, knowing the role ARN is not enough to assume it.

Mode A is the recommended default. Mode B exists because a report is only worth
having if it stays current, and nobody re-runs a CLI every morning.

## What each statement is for

| Statement | Rules it serves | What it resolves |
|---|---|---|
| `NetworkAndComputeShape` | R2, R4, R5, R7 | Real free addresses per subnet, ENI counts, actual volume types and instance families |
| `DatabaseCeilings` | R1, R6 | The parameter group actually attached, so `max_connections` stops being an assumption |
| `QueueAndFunctionConfig` | R3 | The account's real concurrency limit, queue attributes, event source mappings that exist outside Terraform |
| `ScalingShape` | R6 | Scaling policies as applied, which drift from what the plan declared |
| `AccountLimitsAndUtilisation` | all | Service Quotas for real limits, CloudWatch for consumption against them |
| `NeverReadContent` | none | An explicit deny, see below |

## The explicit deny is the point

Every action in `NeverReadContent` is something a broad read-only policy grants
and headroom must never use. The deny is there so that attaching
`ReadOnlyAccess` to the same role later, by habit or by a hurried incident,
still cannot reach them. An explicit deny wins over any allow, from any policy,
in any account.

Four of them are worth naming, because they are the ones people grant without
noticing:

- **`lambda:GetFunction`** returns a presigned URL that downloads your
  deployment package. `GetFunctionConfiguration` returns the settings and no
  code, so that is what the allow grants.
- **`ecs:DescribeTaskDefinition`** returns container definitions, and container
  environment blocks routinely hold credentials. headroom reads the pool size
  out of the task definition **locally, from your plan file**, and never needs
  the API.
- **`ec2:DescribeInstanceAttribute`** returns user data, which is where bootstrap
  scripts keep their secrets. Nothing in the analysis needs it.
- **`ssm:GetParameter*` and `secretsmanager:GetSecretValue`** are how most
  Terraform passes secrets between repositories. They are denied outright.

## Before you attach it

- Replace `REPLACE_WITH_YOUR_REGIONS` with the regions you actually use. The
  condition is a real boundary, not decoration: it stops the role from reading a
  region you never intended to expose.
- Managed policies cap at 6144 characters. If yours is already near the limit,
  split this into two customer-managed policies rather than trimming the deny.
- Nothing here requires write access of any kind. If a future version of
  headroom asks for one, that is a reason to ask why, not to grant it.
- Every call the role makes lands in CloudTrail under the role name, so the
  cheapest audit is to read a day of it.
