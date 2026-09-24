# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).



## [Unreleased]

### Added

- `get_info` reports the team-review endpoint's wiring: `reviews.url`, the gateway the asks and notices go to, and
  `reviews.audience`, the audience of the projected ServiceAccount token it admits (the chart passes `reviews.audience`
  as `REVIEWS_AUDIENCE`); without `REVIEWS_URL` the object stays `configured: false` alone.
- `get_info` reports the declaration vocabulary, `schema: {origin, componentTypes, flavours, languages, visibilities,
  lifecycles}`: the values the validator's repositories schema allows, read from the one schema instance the tools and
  the collector validate against (the copy embedded in the engine's devctl version, which `origin` names); a field that
  cannot be read is `schema.error`, never an empty list.
- The sweep survives a pod restart: each record is written as soon as it is built and checked, and a cursor
  (`inventory:sweep-cursor`, the sweep's start) lets the next pod take an unfinished sweep younger than the interval up
  at once, keeping the records written since its start and reading and checking only the rest; stale records are still
  removed and the summary written only after a complete pass, which now carries `resumes` and `carried`. The log
  reports the sweep's progress (`sweep progress`, every 50 records) and a stop (`sweep interrupted`). The engine's
  checks pause at the REST budget's end until its reset instead of failing with GitHub's rate-limit refusal:
  `inventory.sweep.restBudgetFloor` (default 500; `--rest-budget-floor`, `REST_BUDGET_FLOOR`).

### Changed

- devctl 8.98.1 (was 8.96.0): the engine's `(*reposetup.Schema).FieldValues`
  ([devctl#2405](https://github.com/giantswarm/devctl/issues/2405)) reads `get_info`'s schema lists; the validator's
  schema is built once at startup instead of on every validation.
- The chart's Valkey is the `valkey` app 0.1.7 (was 0.1.4; 0.1.5 lacked its exporter image). From 0.1.5 on the
  subchart's config checksums are pod annotations, so the first upgrade restarts the Valkey pod once.
- devctl 8.96.0: the team-file schema the inventory and `create_repository` validate entries against knows
  `gen.ci.templateContent` ([devctl#2397](https://github.com/giantswarm/devctl/pull/2397)), the field a template
  repository sets beside `generate: false` when its `.circleci/config.yml` is content for the repositories created from it;
  an entry carrying it was refused as an unknown field before. The record's release step and `devctl pr wait` treat a
  repository CircleCI has never run a pipeline for as GitHub-only ([devctl#2396](https://github.com/giantswarm/devctl/pull/2396)).

### Fixed

- The record's release step no longer reads a release built from the default branch's build
  ([devctl#2408](https://github.com/giantswarm/devctl/issues/2408)). A release cut on the default branch head — every
  creation's `v0.1.0` — carries that branch's pipeline's statuses on its commit, and for a repository whose tag
  pipeline the release watch cannot read (private, no `circleci.existingSecret`) the step read `setup` and `go-build`
  green as the release's: `release v0.1.0 built: CircleCI success (1 job)` for a tag no pipeline built, while
  `devctl repo watch` failed on the missed build. The rule `watch_repository` follows since #122 now decides both,
  shared as `inventory.CI.TagOnly`: until a job only the tag pipeline runs (on the tag, not on the default branch) has
  reported, the reconciler run's release step decides when it found the tag unbuilt or red; without such a run, a
  green set of shared jobs reads `unchecked` naming the tag pipeline's own jobs, never built. A declaration without
  tag-only jobs reads as before.

- The chart's Valkey keeps the inventory on a 1Gi ReadWriteOnce PersistentVolumeClaim by default
  (`valkey.valkey.dataStorage`), as the chart's `Recreate` strategy assumed; it lived in an `emptyDir`, so a Valkey pod
  restart emptied it. `make helm-verify` asserts the default render: the volume, and the restricted Pod Security
  Standard contexts on the Valkey pod and every container and init container.
- `watch_repository` ends on a first release no pipeline built instead of waiting for it forever: a creation followed
  from chat never answered. Auto-release tags the scaffold on `main` before the reconciler follows the project on
  CircleCI, so CircleCI never builds that tag; the follow builds `main`, whose head the tag names, and its `setup` and
  `go-build` statuses on the release commit made the `released` phase wait for a chart job no pipeline would run,
  although the reconciler run had reported `missed-tag-build`. Until a job the pipeline runs on the tag and not on the
  default branch (`ci.jobs`) reports, the run's release step decides: a failed step, or a `red-release` or
  `missed-tag-build` finding for the tag, fails the phase with the finding's fix and the run. Once the tag pipeline
  reports — a tag pipeline triggered by hand — its statuses decide as before.

- The `helm.sh/chart` label is valid for any chart version
  ([#115](https://github.com/giantswarm/giantswarm-repo-manager/issues/115)). The label is `<name>-<version>` cut to 63
  characters, and the cut of a long version (a branch build's `X.Y.Z-dev.<branch>.<date>.<time>.h<sha7>`, or the
  `<tag>+<digest>` helm-controller installs) could end in `_` or `--.`, which trimming one `-` and one `.` left in
  place, so the API server refused every labelled object; the whole run of `-`, `.` and `_` at the ends of the cut is
  now trimmed. The chart's render assertions (`hack/verify-chart.sh`, `make helm-verify`) check the label for such
  versions, and the new `chart` workflow runs them with `helm lint` on every change.

- The record's release step no longer counts another pipeline's statuses on the tag commit
  ([#106](https://github.com/giantswarm/giantswarm-repo-manager/issues/106)). For a repository whose tag pipeline the
  release watch cannot read (private, no `circleci.existingSecret`), the step stands in with the commit's `ci/circleci:`
  statuses, and a commit status is per commit, not per pipeline: a branch pipeline that built the release commit — a
  bot's temporary branch, a branch pushed at the tag days later — posted its jobs' statuses beside the tag pipeline's,
  and the step read them as the tag's (backstage v2.58.8: `red-release` from two branch legs beside a green tag
  pipeline; tunnelport v1.6.7: `red-release` from a canceled branch pipeline six days after the tag). The record now
  keeps the declaration's jobs with their filters (`ci.jobs`) and each status's state (`failed`, `pending` beside
  `contexts`), and the step reads the statuses against the declaration: a job the pipeline never runs on the tag is a
  branch pipeline's and is ignored; a failed job that runs on the tag alone is the tag pipeline's failure, named alone
  in the finding; a failed job that runs on branches too cannot be attributed beside a branch pipeline's statuses, and
  the release reads `unchecked` with the token as the fix rather than red or green. Without a branch pipeline's
  statuses every failure is the tag pipeline's, as before. The `circleci` step's sentence about the default branch
  head counts the branch's own statuses the same way.

- The release watch stops asking about a release its team is never told of: a team without a policy file is not
  messaged, and neither is an installation without a team-review endpoint, so the sentence is recorded as told and the
  next pass over the same red tag reads the pipeline without a policy read and a warning each time (before, kyverno-app
  v0.25.0 — red, team-shield without a policy file — warned on every pass). What may change by the next pass (the
  identity, a read GitHub refused, the gateway) is still retried.

### Added

- The record's `circleci` facts carry CircleCI's GitHub webhook
  ([#110](https://github.com/giantswarm/giantswarm-repo-manager/issues/110)). The reconciler's circleci step verifies
  the webhook CircleCI installs on the follow (devctl#2358), without which no push and no tag reaches CircleCI:
  `circleci.webhook` is `false` when the step reports `circleci-webhook-missing`, `true` when a step without changes
  ends its summary with `webhook present`, and absent, with `webhook` in `circleci.unknown`, when no run tells (a failed
  step, hooks the run's identity cannot read, a check that plans the follow, a step with changes, a run of an engine
  that did not verify the webhook yet). `devctl repo status` prints `webhook present` or `webhook missing` in its
  `circleci` line. The record's circleci step keeps the reconciler's findings whatever its verdict, so a repair that
  followed a project CircleCI then left without its webhook reads `reported` with `circleci-webhook-missing` and its
  fix, not `ok`; a converged step's summary names the webhook.

- A release nothing was published for is told to the team once, within minutes, whoever merged and however
  ([#107](https://github.com/giantswarm/giantswarm-repo-manager/issues/107)). The release watch
  (`inventory.releases.interval`, default 5m; `"0"` off) follows the latest release of every declared repository
  from its tag to the end of the tag's own CircleCI pipeline: one GraphQL query per pass over the most recently
  pushed repositories, and for every release still running the tag's pipeline on CircleCI, found by its `vcs.tag` —
  never a branch pipeline at the same commit, which the commit's statuses cannot tell apart — its workflows (the
  newest run per name) and the jobs of a failed one. A red pipeline is one sentence to the team's `standupChannel`
  naming the tag, the pull request behind it, the failed jobs and how they failed, the rerun from failed as the fix
  and `devctl release wait` to confirm, linking the failed workflow; a tag without a pipeline
  `inventory.releases.grace` (default 10m) after its creation is the missed build, told the same way. The record's
  `setup.release` holds the watch's state and the sentence told, so a later pass over the same red tag says nothing,
  a rerun that goes green turns the record built silently, and the next tag is a new watch; the record's `release`
  step and findings (`red-release`, `missed-tag-build`) follow the watch once it has read the tag, so the Repositories
  page agrees with the notice and a branch pipeline at the release commit no longer reads as `red-release` for a
  watched repository ([#106](https://github.com/giantswarm/giantswarm-repo-manager/issues/106) for public
  repositories). Public repositories are read without a token; a private one needs `circleci.existingSecret` (a
  CircleCI API token under `token`) and reads `unchecked` without it — nothing is posted. `get_info` reports
  `circleci.tagPipelines`: anonymous, token or off.

### Changed

- The engine is devctl v8.91.7, from v8.89.0: the embedded schema declares `gen.ci.chartReleaseGateJob`
  (devctl#2382), so an entry that sets it, giantswarm/agent-platform's, is validated and checked instead of refused as
  `entry-refused` with no set-up checks; a created repository's scaffold passes the key to `gen circleci`
  (devctl#2383), which gates the release chart push on a repo-owned job (devctl#2363); the generated CircleCI
  configuration pins architect orb 10.6.3 (devctl#2362).
- The engine is devctl v8.89.0, from v8.87.3: the circleci step verifies CircleCI's webhook (devctl#2358), a chart
  scaffold carries the smoke test the app test suite runs (devctl#2356), and the `plans` flavour with the template
  `giantswarm/template-plans` (devctl#2355).
- The engine is devctl v8.87.3, from v8.87.1: the CircleCI client reads public projects without a token
  (`Config.Anonymous`), the engine's release step counts only the newest run of every workflow, and the `red-release`
  fix names the rerun from failed (devctl#2353).
- The engine is devctl v8.87.1, from v8.86.1: a ruleset's bypass list is compared only when the identity can read
  it (GitHub returns `bypass_actors` to write access alone), the summary saying `bypass actors not readable by this
  identity, not compared` otherwise, so a devctl App id passed to the read-mode engine no longer reads every aligned
  repository as bypass-list drift (devctl#2346); a disabled ruleset raises no `foreign-ruleset` finding (devctl#2344);
  the entry declares the rulesets a team keeps (devctl#2349).

### Fixed

- The engine's read-mode checks pass no devctl App id by default (`inventory.engine.devctlAppID: 0`): GitHub shows a
  ruleset's bypass actors to identities that administer the repository, and the inventory App reads, so with the id
  (0.26.0) the protection step compared an empty list against the reconciler's three actors and reported every aligned
  repository as `protection drift: bypass actors: App …, repository admins …, team …`, giantswarm/devctl included, whose
  ruleset carries exactly those. The step compares the rules and says `bypass actors not compared` (devctl v8.86.1); the
  id stays a setting for a read identity that sees the actors (devctl#2346 asks the engine to tell an unreadable list
  from an empty one).

### Changed

- The engine's read-mode checks run with the devctl GitHub App's numeric id (`--devctl-app-id`, `DEVCTL_APP_ID`; the
  chart's `inventory.engine.devctlAppID`, default 5025978, the App of the reconciler in giantswarm/github), so the
  protection step compares the ruleset `devctl: default branch` in full, its bypass list included (the devctl App, the
  owning team and the repository admins for pull requests, as the reconciler writes them), and plans a repository still
  on classic protection as the reconciler would: create the ruleset, remove the classic protection. The check writes
  nothing. The engine is devctl v8.86.1, whose protection step reads the ruleset first without the id and compares
  the rules alone (devctl#2341); on devctl 8.85.x it reported every aligned repository as `protection drift: protect
  main …` with the advisory
  `rulesets-not-enabled`, "not converged", although the reconciler's nightly run had written the ruleset and removed the
  classic protection. The create path (`create_repository`, repair mode as the person) is unchanged: a new repository
  gets classic protection, which the reconciler's next run moves to the ruleset.

### Fixed

- `create_repository` resumes an interrupted creation with the entry as the creation renders it, defaults written out
  (`gen.ci.generate: true`). Since devctl v8.85.4 an existing-mode validation reads an entry as declared, so a resume of
  an entry whose `gen.ci` block leaves `generate` unset was refused (`gen.ci.generate: required`) where the first
  attempt had accepted it; `update_repository` of such an entry is refused as the schema says, the refusal naming the
  field.
- A reconciler run's findings reach the team's standup channel only when they are news, after three
  failure modes filled the channel with 26 messages in two bursts of which roughly four were chores. A
  finding of kind `pending-pull-request` is no longer posted: the `codeowners` step opens that pull
  request in the very run that reports it and the bot-PR sweep merges it, so "merge the pull request" asks
  for work nobody has to do — eight of the 26 were CODEOWNERS pull requests the engine had opened minutes
  earlier, none of which a person merged after reading the message. A finding is verified against the
  record's live check before it is posted: the poller reads a run's artifact up to five minutes after the
  run finished, the collector runs the engine's read-mode checks in the same refresh, and per kind the
  step reported the live check's findings are the ones posted, in its words — none when it found the step
  clean, and the run's findings when it skipped the step, failed it or has no result for it, so
  `missed-tag-build` and `red-release` still reach a team whose manager has no CircleCI client. One of the
  26 told a team to merge a pull request a person had merged six minutes and forty-five seconds earlier.
  And a finding is told once: `setup.told` holds the sentences the team has heard, so a standing decision
  or chore is no longer re-posted on every merged change of the entry — ten of the 26 were advisory
  `foreign-ruleset` findings across seven repositories, re-reported by an alignment opt-in that had
  nothing to do with them — and is told again only after it has gone away and come back. Failed steps and
  the sentence about the change are unchanged.

- The engine is devctl v8.82.7, from v8.82.1. Every declared repository read *not in sync* with `settings drift:
  allow_squash_merge false → true, allow_update_branch false → true, allow_auto_merge false → true, delete_branch_on_merge
  false → true` while the reconciler's run said `settings ok`: `GET /repos/{owner}/{repo}` carries the six merge settings for
  an identity with admin rights on the repository only, the inventory App (`administration: read`) got them as `null`, and
  the engine read an absent field as `false`. The engine's settings step now reads them through GraphQL, which answers the
  inventory App with the values, and reports them as an `unchecked` finding, never as drift, when that read fails too; the
  inventory App stays read-only. A refused entry's check reads `converged: false` (the engine's `Refused` result, devctl
  v8.82.6) instead of *converged* beside the refusal.

### Added

- `adopt_repository` (`repository`, `team`, `entry`, `reason`; `dryRun` / `mode: commit`) declares a repository that exists on
  GitHub and no team file declares — the inventory's unassigned scope — the way `create_repository` declares a new one, without
  the create and scaffold steps: the entry shaped and rendered by the same code, validated against the repositories schema alone
  (the engine's existing-repository mode; the creation rules are for repositories the manager creates), inserted into
  `repositories/<team>.yaml` by the same insert as the creation-only pull request, opened as the caller with auto-merge armed, the
  ask with the Approve button delivered to the team's channel (a plain addition, so the team reviews it), the record's expected
  run marked `added`. `align` is the person's to set and the pull request body says what the merge does with and without it.
  `lifecycle: deprecated` or `archived` in the entry adopts the repository and ends its life in the one pull request
  (`chore(repositories): adopt and archive <name> into <team>`); the entry then gets `align: true` beside the lifecycle, as
  `set_lifecycle` does, and the plan and the ask say so; `deleted` is refused (declare first, then `set_lifecycle` with the name
  typed). Refused before any write when the name is free on GitHub (a creation) or declared already (the refusal names the team and
  the tool to use) (giantswarm/giantswarm-repo-manager#90).

### Fixed

- A reconciler run that completes without a report for a repository expecting one — its `Reconcile <name>` job failed or was
  cancelled before the report step, a devctl download answered 504 — ends the record's pending run in the poll that reads it,
  not 15 minutes after the dispatch: the run's jobs name the repositories it handled (the REST payload carries no dispatch
  inputs), and a record whose pending run this run is — a `workflow_dispatch` run created at or after the Align now's mark, the
  `push` run whose head is the merge commit of the record's pull request — gets `setup.missingRun {runUrl, conclusion}` and the
  finding `reconcile-run-missing` naming the run, its conclusion and "uploaded no report", its fix the run and
  `align_repository`. The Repositories page's alignment dialog shows the failure at once. A success run without a report over
  a repository expecting none stays a log line; a run over many repositories that failed on some gives up those alone.
- The poller reads a run's artifacts `concurrency` at a time (the sweep's setting) instead of one after another: a run over 95
  repositories reached the records in about twelve minutes, each artifact's refresh with its engine check in series.

### Changed

- The engine is devctl v8.82.1, from v8.67.0: the repository-alignment baseline — status checks required non-strictly with
  `enforce_admins` final, advisory findings that leave `converged` true, the finding `missed-tag-build` in place of a triggered
  tag build, the `circleci` and `release` steps only for a repository with a pipeline, the profiles `fork` and `customer` with
  `defaultBranch` and `requiredChecks` in the entry, and the run's request count under `requests`. The inventory follows the
  engine: a check's `converged` is the engine's rule (a non-advisory finding on a reported step clears it, an advisory one
  does not), the record's findings carry `advisory`, the clientless `release` step reports `missed-tag-build` instead of
  planning a trigger, and an entry the tools write orders `defaultBranch` and `requiredChecks` as the team files do
  (giantswarm/giantswarm-repo-manager#89).
- `create_repository` resumes only a creation of the caller's interrupted after the create or the scaffold step: a repository the
  caller administers whose default branch carries at most one commit. One with a history is somebody's work: its taken name
  stays refused, the refusal naming `adopt_repository`. Before, any repository the caller administered was declared by
  the resume, and the pull request then said it had been created and scaffolded by the caller
  (giantswarm/giantswarm-repo-manager#90).

- `set_lifecycle` takes `deleted`, the fourth lifecycle: the reconciler unfollows the repository on CircleCI and
  deletes it on GitHub — code, issues, pull requests, releases and packages with it; an organization owner can restore
  it on GitHub for 90 days — and the entry stays in the team file as the record of the deletion. A deletion needs
  `confirm`, the repository's name as the person typed it (with or without the org), and is refused without it or
  with another name; the pull request (`chore(repositories): delete <name> (<team>)`), the ask and the record's
  expected run (`kind: deleted`) say what happens, and the team's standup channel hears "<person> deleted the repo
  <name>" after the run. `list_repositories` filters `lifecycle: deleted`, and the boolean `archived` counts a
  declared deletion among the repositories whose life is over (hidden by the page's default). devctl 8.67.0, whose
  engine handles the lifecycle (giantswarm/giantswarm-repo-manager#87).

### Changed

- `align_repository` on a declared repository that has not opted in is the opt-in, not a check: the answer's `mode` is `opt-in`, nothing is dispatched, and the tool opens the team-file pull request that sets `align: true` in the entry as the caller — nothing else in the entry changes, auto-merge is armed, the ask with the Approve button goes to the owning team's channel (a member other than the caller approves), the record marks the expected run — so that the reconciler's run of the merge aligns the repository. The answer carries `optIn.plan` (the entry before and after, the pull request, the ask) and, after the commit, `optIn.committed` (pull request, ask delivery, pending run); `then` and `warning` say what happens on the merge. An opted-in repository is dispatched as before (`align`), one without an entry is checked from the team alone (`check`). A field an edit adds to an entry (`align`, `lifecycle`) lands before the entry's `gen` block, where the team files keep it, instead of after it (giantswarm/giantswarm-repo-manager#85).
- The opt-in to alignment is the repository's own: `align: true` in its entry in `repositories/<team>.yaml`. `create_repository` and `validate_repository` write it into every entry they create — the creation is the opt-in, so the reconciler run after the creation-only pull request sets the repository up — and refuse an entry saying `align: false` (the dry run's rendered entry and the pull request's diff show the field). `set_lifecycle` writes it beside the lifecycle when the entry lacks it, and the pull request and the ask say so (without it the reconciler would record the lifecycle and apply nothing). `align_repository` answers from the declaring entry: `optedIn` is its `align`, `mode` is `align` with it and `check` without, the answer carries `declared`, and the warning names the repository — how to opt in (`update_repository`, the team reviews) for an entry without the field, a check from the team alone for a repository without an entry. The team's policy file `repository-setup/<team>.yaml` holds the two channels only; `alignOptIn` is read nowhere. devctl 8.66.0, whose embedded schema knows `align` (giantswarm/giantswarm-repo-manager#80).

### Fixed

- The `circleci` step no longer says "CircleCI does not build <repository>" beside an `ok` when the reconciler's run found the project followed and the head simply carries no status yet: the summary then reads "no CircleCI status on main's head yet"; the conclusion is drawn only when nothing else has read the project.

### Fixed

- `ci.platforms` / `ci.arm64`: an image push on orb 9 or newer without a `platforms` value and without a go-build job resolves to the orb's built-in default (`linux/amd64,linux/arm64`), the list `push-to-registries` falls back to when no `.platforms` file was written, instead of staying unknown.

### Added

- The record's `ci` block — what the repository's CircleCI configuration on the default branch says, read with the repository (`.circleci/config.yml`, `workflows.yml`, `custom.yml`): `generated` (devctl's header), `orb` (the giantswarm/architect version pinned), `imagePush`/`chartPush`, `platforms` and `arm64` (the image platforms resolved the orb's way: the push job's `platforms`, the per-architecture build-image jobs it merges, go-build's list from orb 9, the legacy multiarch rule; absent when the configuration does not say), `chinaPush` (`split` with `split-china-push`/`sync-china-registry`, `inline`, `custom` with `registries-data`, `none`) and `signing` (`signed` for a public repository whose push jobs keep the orb's `sign: true` default since 8.2.0; `unsigned` with the reason — private repository, `sign: false`, an older orb; `unknown` without the orb or on a development version; `none` when nothing is pushed). `list_repositories` rows carry `ci` and take the filters `orb` (a version or its prefix), `arm64`, `chinaPush`, `signing` (giantswarm/giantswarm-repo-manager#78).
- `reality.latestRelease.build`: the release tag commit's `ci/circleci:` statuses, read in the sweep — whether CircleCI built the release.

### Changed

- The set-up steps `circleci` and `release` answer what a person reading them asks, from GitHub alone: `circleci` is `ok` when the head carries CircleCI statuses ("CircleCI builds main: success (6 jobs, …)") and the engine's follow plan as `drift` when it carries none; `release` is `ok` when the tag commit's statuses are green, `reported` with `red-release` when they failed, and the missed tag build as `drift` when the commit carries none. The reconciler's stored run still decides when it ran (it read the settings only a token reaches). No `unchecked` finding about setup workflows or checkout keys any more; `unchecked` remains for status contexts truncated before a CircleCI one (giantswarm/giantswarm-repo-manager#78).

### Removed

- The CircleCI token path of 0.18.0 — the chart value `circleci.existingSecret`, the flag `--circleci-token` / `CIRCLECI_TOKEN`, the engine's CircleCI client, `get_info`'s `circleci.configured`: none of the answers needs a CircleCI token, and a CircleCI personal token cannot be scoped to reads.

### Fixed

- A team-file pull request whose base moved under it — a neighbouring entry changed first: a repository declared next to the one being archived, two lifecycle changes side by side — no longer waits for a rebase by hand. `approve_change` reads GitHub's `mergeable` and, for a conflicting pull request, re-renders it on the current base before approving: the entries the pull request changes (against its merge base, per team file) re-applied to the files as they read now, the branch force-pushed as the approver with that one commit, the pull request, its ask and its auto-merge kept; the answer carries `rerendered` and its sentence names the re-render. The poller notes a conflicting open pull request on the record (`setup.pendingRun.conflictsSince`, dropped once it is mergeable again) and, for one approved already — its ask spent, its auto-merge unable to fire — posts a fresh ask to the deciding team's channel, once, whose Approve re-renders and lands it; a pull request awaiting its review keeps its standing ask, which does the same on the click (giantswarm/giantswarm-repo-manager#76).
- The expected run of a pull-request-driven change (a creation, a lifecycle change, a transfer, a configuration change) is awaited from the merge, not from the opening. While the pull request is open the record's `setup.pendingRun` waits without a deadline and the poller keeps its five-minute tick; the poller reads the merge from the pull request as the inventory App, stores it on the mark (`pendingRun.mergedAt`) and counts the pending window from there; a pull request closed without a merge drops the mark. A late merge — a validation run re-run after a rate limit, a team review — no longer leaves the finding `reconcile-run-missing` on a run that starts seconds after the merge, and `watch_repository` no longer answers `failure {phase: setUp}` for it: an open pull request keeps the watch `pending` on `merged`, and a watch that sees the merge wakes the poller. A run that does go missing after the merge is reported with the merge time; an Align now keeps its window from the dispatch (giantswarm/giantswarm-repo-manager#75).
- A Go service with the `app` flavour is scaffolded with template-app's chart at `helm/<name>/`: the engine is devctl v8.65.8 (`get_info` reports it); with the older engine the repository was created without its chart and its first release was red. The scaffold plan's sentence names the chart (giantswarm/giantswarm-repo-manager#66).
- `set_lifecycle`, `transfer_repository` and `update_repository` in `mode: commit` leave the record expecting the reconciler run of their pull request — `setup.pendingRun {dispatchedAt, by, kind: archived | deprecated | transferred | changed, pullRequest {number, url}}`, the mark a creation leaves with `kind: created` — so the poller reads the run's artifact, and the team hears its sentence, within the pending interval of the run's completion rather than at the five-minute tick. The answer carries `pendingRun`; the finding `reconcile-run-missing` names the pull request by its kind (giantswarm/giantswarm-repo-manager#63).
- The set-up steps `circleci` and `release` of a repository's `setup.checks` no longer read `skipped: no CircleCI client` — the engine runs without a CircleCI token here, and the inventory now writes those two steps from what it knows: the reconciler's last run over the repository (followed, setup workflows, checkout key, the tag's build — checked with its token) when it ran, and the `ci/circleci:` statuses on the default branch head, which say whether CircleCI builds the branch. Each summary names its source and time; a repository no run has checked yet carries the finding `unchecked` naming what is out of reach and Align now as the fix; a head without a CircleCI status is the drift the engine would report for an unfollowed project. `converged` follows the rewritten steps.

### Added

- A debug channel receives every ask and notice: `reviews.debugChannel` (flag `--reviews-debug-channel`, `REVIEWS_DEBUG_CHANNEL`; a name `reviews.channels` resolves or a Slack channel ID, default empty) redirects every `POST /reviews` and `POST /notices` from the channel the team's policy file names to that one channel, the text closing with the channel the file chose — `… (for #team-x)`, or the ID when the file carries it — so a test round of the flows (create, align, archive, transfer) disturbs no team and a reader tells the redirect from a misconfiguration. The Approve button, the link and the tools' answers are as without it; a debug channel without an ID refuses to start; `get_info` reports `reviews.configured` and `reviews.debugChannel` (giantswarm/giantswarm-repo-manager#50).
- `watch_repository`: a read-only tool that follows a new repository to readiness after `create_repository`, blocking up to `timeout` seconds (default 120, at most 150 — under the MCPServer's 180 s) and reading GitHub as the caller every five seconds. It returns when the repository is ready, when a phase fails, or when the timeout runs out — with the phases reached either way, each with its timestamp and the seconds since the phase before: `created`, `scaffolded`, `declared` (the pull request is open and names the repository), `merged` (closed without a merge fails), `setUp` (the reconciler run of that pull request, from the record: a failed step or a refused entry fails the phase, a run given up as missing fails it with the finding, the run's other findings travel as `findings`) and `released` (the first release exists and the CircleCI statuses on its commit are green; a failing status fails the phase; while CircleCI has not reported on the release's commit the run's release step decides — a failed step or a `red-release` finding fails, anything else waits). The answer carries `ready`, `pending` (the phase still waited for), `failure {phase, reason}`, `repository` and `pullRequest` (URLs), `release {tag, url}` and `waited` (giantswarm/giantswarm-repo-manager#51).
- `create_repository` in `mode: commit` leaves the new repository's record expecting the reconciler run of its declaration pull request — `setup.pendingRun {dispatchedAt, by, kind: created, pullRequest {number, url}}`, the mark an Align now leaves with `kind: dispatched` — building the record from GitHub first when the inventory has not seen the repository, so the poller reads the run's artifact within its pending interval instead of the five-minute one; a run that does not report within 15 minutes leaves the finding `reconcile-run-missing`, worded for a creation (the pull request may not have merged yet). The answer carries each repository's `pendingRun` and `then`, which names `watch_repository` (giantswarm/giantswarm-repo-manager#51).

- The team's approval lands the change. Every pull request the write tools open (`update_repository`, `transfer_repository`, `set_lifecycle`) is armed for GitHub's auto-merge as the author (a squash, the team-files repository's one merge method), so it merges the moment a member's approving review is in and its checks are green — from the Slack Approve button or from GitHub alike (`pullRequest.autoMerge` in the answer). `approve_change` then lands the pull request it approved: merged as the approver when GitHub lets it, else left to auto-merge, armed if it was not; the answer carries `merged`, `autoMerge` and a one-sentence `message` for the channel the button was clicked in — "Approved as <login> and merged: giantswarm/github#N.", "…merges by itself once its checks pass.", or why neither happened and that it is merged on GitHub by hand.

### Changed

- `watch_repository` also returns as soon as a phase completes that was not complete when the call started, with `changed: true` — the caller narrates the new phase and calls again, so a merge 80 s into a two-minute wait is told within a poll interval rather than when the wait runs out. Ready and a failure end the call as before, `timeout` stays the upper bound, and a call that ends ready, failed or on its timeout without a new phase carries `changed: false` (giantswarm/giantswarm-repo-manager#73).
- `approve_change` refuses the person who opened the pull request before GitHub does, in words the person can act on: "<login> opened giantswarm/github#N, and GitHub does not accept an author's approval of their own pull request; another member of <team> has to approve" — the asker's own click on the Slack Approve button now reads that instead of GitHub's `Can not approve your own pull request`. The answer carries the pull request's `author`.
- An ask closes with who decides — "A member of <team> other than <asker> approves." — the reason reads "Reason: …", and the pull request URL travels only as the message's link (the gateway's *Open PR* button, the notice's *Open PR* line) instead of once more in the text.

- `reconcile_repository` is `align_repository` (*Align now*). Its description warns what an alignment changes on the repository — merge settings, wiki and projects, team permissions, branch protection with `enforce_admins` and the required checks, the CircleCI follow, a CODEOWNERS pull request, description and visibility, lifecycle, catalog, a missed release build — and that it runs as the caller. The answer (dry run and commit) carries `mode` (`align` when the owning team has opted in, `check` otherwise), `optedIn`, `team`, the `planned` changes of the inventory's last check per step with `checkedAt`, and a `warning` paragraph for the person to read before confirming. The policy file's opt-in key is `alignOptIn` (was `repairOptIn`); a file with the old key reads as not opted in (giantswarm/giantswarm-repo-manager#46).

### Added

- The muster pin takes an optional scope parameter: `muster.mcpServer.auth.authorizationServer.scopes` (space-separated; default empty omits the field) renders as the MCPServer's `authorizationServer.scopes`, for an authorization server that needs one — a Dex standing in for GitHub in a lab takes `openid profile email offline_access`; GitHub takes none, a GitHub App's permissions are the App's. The lab shape is the fixture `tests/lab-oauth-values.yaml` (giantswarm/giantswarm-repo-manager#24).
- The inventory pulls the reconciler's runs from GitHub as the inventory App: every `inventory.reconciler.pollInterval` (default `5m`; flag `--reconciler-poll-interval`, `RECONCILER_POLL_INTERVAL`) the poller lists the completed runs of `inventory.reconciler.workflow` (default `reconcile-repositories.yaml`) in the team files repository, downloads each run's `reconcile-<repository>` artifact — the redirect to the signed blob URL is followed without the installation token — and stores it as the repository's `setup.lastRun` (`source: reconciler`, with `runId` and `attempt`). The cursor `inventory:reconciler` survives a restart; no artifact is read twice (run id + attempt), a re-run is read again, an artifact older than the stored run does not replace it, an open run holds the cursor until it completes. The first poll reads the last seven days.
- `reconcile_repository` leaves `setup.pendingRun {dispatchedAt, by}` on the record (a `workflow_dispatch` returns no run id); the poller runs every 30 s while one is pending; the repository's next artifact clears it. After 15 minutes without one it becomes `setup.missingRun` with the finding `reconcile-run-missing` naming the workflow's Actions page, cleared by the next artifact or dispatch. `list_repositories` rows carry `setup.pendingRun`; the dispatch result carries `pendingRun`.
- `sweep_inventory`: the full sweep over the org on demand, for a member of the teams that own the manager and the reconciler — `inventory.sweep.teams` (default `[team-bumblebee, team-planeteers]`; flag `--sweep-teams`, `SWEEP_TEAMS`), the membership checked on GitHub as the caller the way `approve_change` checks it, a non-member refused in the same words. The answer carries `started` (false while a sweep already runs), `running` and the last sweep's summary; the sweep writes the inventory cache alone, nothing on GitHub (giantswarm/giantswarm-repo-manager#29).
- The muster pin carries `expectedIssuer` (`muster.mcpServer.auth.authorizationServer.expectedIssuer`, default `https://github.com/login/oauth`; empty omits the field) so GitHub's RFC 9207 `iss=https://github.com/login/oauth` passes muster's issuer check while the grant stays filed under the App's own `issuer` (giantswarm/muster#1277).
- `create_repository` in `mode: commit` creates the repository and pushes its scaffold as the caller, then opens the declaration pull request — the order create → scaffold → PR of the repository set-up design v3: the engine's standalone create and scaffold steps (devctl `v8.65.0`, `reconcile.Runner.Create`) run with the caller's own GitHub token, so the repository exists and the caller is its admin before the team-file entry is reviewed; the reconciler sets it up after the merge and never creates. The result carries each repository's URL, its scaffold commit and the pull request; `v0.1.0` follows from the scaffold's auto-release. Refusals come before any write, with the engine's texts: the creation rules, a taken name, and a caller who is not an owner of the org (the org lets only owners create repositories). A creation interrupted by a failure resumes: a repository the caller administers is not created again (validated for an existing repository), a scaffold on the default branch is not pushed again, an open pull request for the branch is reported (`pullRequest.existing`). `validate_repository` and `dryRun: true` plan the three writes (`creation`: the create and scaffold steps in check mode, the pull request, `resumed`, or the `refusal`) and write nothing. Chart and server: the scaffold is rendered by the engine from the templates on GitHub, downloaded with the caller's token.
- The nine tools: `validate_repository` (the engine's dry run with the guard notices `team-review` and `batch-review`, the author's teams read as the person), `create_repository` in `mode: commit` (the creation-only pull request as the caller), `update_repository`, `transfer_repository` (one pull request over both team files, naming the giving and the receiving team), `set_lifecycle` (`deprecated` | `archived`), `approve_change` (team membership checked, the approving review as the caller) and `reconcile_repository` (the `reconcile-repositories.yaml` dispatch as the caller). Every write takes `dryRun` and `mode`; `commit` is the only mode, `apply` is refused. Team files are edited byte for byte.
- `list_repositories` scopes per caller (`mine` | `team` | `unassigned` | `all`; the caller's teams from their GitHub grant, else the IdP groups) and takes the Repositories page's filters (search, renovate, team incl. none, visibility, fork, lifecycle, inactiveDays, decision).
- Asks and messages through klaus-gateway's team-review endpoint (`POST /reviews`, `POST /notices`; the pod's projected ServiceAccount token for audience `klaus-gateway`): lifecycle and transfer asks with an Approve button calling `approve_change`, the notice to a transfer's giving team, the completion message after a reconciler run (`repository · catalog entity · first release`). Chart values `reviews.gatewayURL`, `reviews.audience`, `reviews.channels` (name → Slack ID for policy files that carry a channel name), `teamFiles.repository`, `teamFiles.ref`.
- `docs/tools.md`: every tool's description and input schema, kept current by a test.

### Removed

- The orphan score and the decision note: `orphan` (score, reasons, `stalePeriod`) on the record and on `list_repositories`' rows, the arguments `minOrphanScore`, `decision` and `stalePeriodDays` (`list_repositories`, `get_repository`), `lifecycle: none`, the tool `decide_repository` and `decision` on the record. A record stored with the earlier shape still loads (the fields are ignored). Breaking for a client that read `orphan` or `decision` or called `decide_repository`.
- `POST /internal/refresh` and the reconciler workflow's call to it. On the installation the server is reachable through muster alone — nothing routes `/internal/*` from outside the cluster, every POST answered 404 — and publishing the path behind the shared static token would have put a write endpoint on the internet behind one secret. What the POST carried already exists on GitHub as the run's artifact, so the inventory pulls it.
- `GET|POST /internal/sweep`, the static token `INTERNAL_TOKEN` / `--internal-token` and the chart's `inventory.internal.existingSecret`: the server has no inbound path but muster's and no shared secret anywhere; the sweep on demand is the tool `sweep_inventory` (giantswarm/giantswarm-repo-manager#29).

### Changed

- The message after a reconciler run is one sentence about the change for the team, posted to the team's `standupChannel`: `alice created a new repo: bumblebee-repo (app, go)`, `alice added the existing repo bumblebee-repo (app, go) to team-bumblebee`, `alice transferred the repo bumblebee-repo (app, go) from team-planeteers to team-bumblebee`, `alice archived the repo bumblebee-repo`, `alice deprecated the repo bumblebee-repo` — rendered from the artifact's `change` block (`kind`, `by`, `pullRequest`, `fromTeam`: the reconciler's classification of the merged pull request) and the declaration's flavours and language, linking the pull request. A failed step and a finding of that person's run post one sentence each with what to do, linking the run. A run nobody's change is behind — a Reconcile now, the schedule, an artifact without a `change` block — posts nothing, findings and failures included: the reconciler doing its job is not news, and the nightly's findings would repeat every night; they stay on the record and in the run's summary per team. An edit a person made (`changed`) posts its failed steps and findings alone. A finding of kind `unchecked` — a check the reconciler's own token could not run — is the platform's, not the team's, and is never posted; it stays on the record. The state line (`repository · catalog entity · first release`) is gone (giantswarm/giantswarm-repo-manager#39).
- `setup.lastRun` keeps the artifact's `change` block as `lastRun.change`.
- Notices — the giving team's transfer notice and the messages after a reconciler run — go to the team's `standupChannel`; asks with an Approve button (archive, deprecate, an incoming transfer) stay in `slackChannel`. The policy file `repository-setup/<team>.yaml` must name both: a file without `standupChannel` is refused the way one without `slackChannel` is, nothing stands in for a missing channel. Chart: `reviews.channels` needs an entry per standup channel name as well.
- Lifecycle and transfer asks name the asking person: `alice asks to archive `giantswarm/x` (owned by team-x).`, `alice asks to transfer `giantswarm/x` from team-x to team-y: your team receives it.`
- `list_repositories` filters as the Repositories page needs them: `team` applies in every scope — under `mine` it narrows to one of the caller's teams, another team selects no rows and the answer's `note` says so (before, `team` was dropped under `mine`); `lifecycle` takes `active` (no lifecycle declared and not archived on GitHub), `deprecated` (declared) and `archived`, where a repository archived on GitHub counts as archived whether or not its declaration says so (before, `archived` missed the GitHub-archived repositories declared without a lifecycle or not declared at all); the new boolean `archived` drops (`false`) or keeps only (`true`) the archived repositories, independent of `lifecycle`; rows come sorted by repository name, case-insensitive (before, by orphan score). Every filter value has a table test.
- The period a Renovate pull request or commit counts as activity within (`renovate: active | inactive`) is `inventory.renovate.activeDays` (flag `--renovate-active-days`, `RENOVATE_ACTIVE_DAYS`; default 180 days), in place of `inventory.orphan.staleDays` / `--stale-days` / `ORPHAN_STALE_DAYS`; it is judged when the rows are read, not stored on the record.
- The inventory App's documented permissions (chart `githubApp.appID`, `README.md`) gain Checks (read): the engine's protection step reads the default branch head's check runs to learn which conditional checks exist; without it every repository carries the finding `unchecked` — the checks on main not readable with this token (403). The permission is granted in GitHub's UI by an org owner.
- The unattended reads run on the read-only App `giantswarm-repo-manager-inventory` alone (giantswarm/giantswarm-repo-manager#18): `githubApp.*` documents it (installation token; Administration, Contents, Pull requests, Issues, Commit statuses, Metadata and Organization members read; the PEM key under `private-key` of `githubApp.existingSecret`); the `giantswarm-align-files` wording is gone. `get_info` names the identity as `inventory.identity` — `app giantswarm-repo-manager-inventory (installation <id>)`, or `not configured` — and the tools that need it say so; nothing stands in.
- The inventory's CircleCI facts come without a CircleCI token: whether CircleCI builds a repository from the `ci/circleci:` commit statuses on its default branch head (read with the repository in the sweep's GraphQL query — the inventory App's Commit statuses permission — next to the `.circleci/config.yml` blob), whether the project is followed and setup workflows are on from the reconciler's run artifact posted to `POST /internal/refresh` (its `circleci` step). The record's `circleci` carries `head` (state, contexts, time), `source` (`statuses` | `artifact` | `statuses+artifact`) and `unknown` (the facts no source yields); `lastPipeline` is gone — it is not derivable. `get_info` reports `circleci.source: statuses+artifact`. The engine's read-mode checks run without a CircleCI client (their `circleci` and `release` steps are skipped).
- A team-file read as the person that GitHub answers with 404 or 403 names the credential and the fix (giantswarm/giantswarm-repo-manager#11): `reading giantswarm/github as <login> failed (404): the repository is not reachable with this credential — your authorization of the App giantswarm-repo-manager does not reach the repository: the App must be installed on all repositories (an org owner's setting), or your own access does not include it`; a file missing on the branch stays `giantswarm/github: <path> not found in <ref>` (one probe of the repository tells them apart). `get_info` reports `teamFiles.readable: true | false | unknown` for the caller's credential (one probe per token, cached 15 minutes) with the reason.

### Removed

- The CircleCI token: the flag `--circleci-api-token` / `CIRCLECI_API_TOKEN`, the chart value `circleci.existingSecret` and its `Deployment` env, the CircleCI HTTP client in `internal/collect`, `get_info`'s `github.circleciConfigured` and the sweep summary's `circleciCalls`.
- The `GITHUB_TOKEN` / `--github-token` development fallback for the unattended reads: the inventory App is the one identity.

- The manager is an OAuth-protected MCP server behind muster (giantswarm/giantswarm-repo-manager#17, #13): the bearer of every call is the person's own GitHub user token — muster runs the consent for the GitHub App `giantswarm-repo-manager` once (the chart's MCPServer pins it as the authorization server: issuer identity `https://github.com/apps/giantswarm-repo-manager`, GitHub's endpoints, `clientCredentialsSecretRef: giantswarm-repo-manager-oauth-client`, `grantScope: subject`), stores and refreshes the token and puts it on every call — verified with `GET /user` (cached 15 minutes by the token's hash) and used for every GitHub call as the person. No bearer, or one GitHub refuses, is a 401 whose challenge names the protected-resource metadata and whose body names the sign-in (`core_auth_login server=giantswarm-repo-manager`); the metadata names the pinned App. `get_info` reports `caller` (login, id) and `auth` (`mode: bearer`, `authorizationServer`) in place of `github.grant`; `list_repositories` scopes by the caller's GitHub teams alone. Gone: the Dex verification (the mcp-oauth resource server), the muster broker client (RFC 8693), the flags `DEX_*`, `MUSTER_URL`, `BROKER_*`, `OAUTH_TRUSTED_AUDIENCES`, `OAUTH_ALLOW_PRIVATE_URLS`, `SSO_ALLOW_PRIVATE_IPS`, `OAUTH_ALLOW_PUBLIC_REGISTRATION`, the chart values `oauth.dex.*`, `oauth.existingSecret`, `oauth.trustedAudiences`, `oauth.sso`, `oauth.allowPublicClientRegistration`, `muster.url`, `broker.*` and `muster.mcpServer.auth.forwardToken` / `requiredAudiences` (now `muster.mcpServer.auth.authorizationServer.*`), and the Dex client Secret template. New: the flag `OAUTH_AUTHORIZATION_SERVER`; `oauth.baseURL` defaults to the in-cluster Service URL. The harness scenario `giantswarm-repo-manager-oauth-mode` replaces `giantswarm-repo-manager-broker-grant`.
- devctl v8.64.8 (from v8.64.3): a declaration the engine refuses is the engine's `Refused` result — the `entry` step reported, one finding per problem (`entry-refused`, `gen-circleci-refused`) naming the field to fix — in place of the inventory's own `declaration-refused` finding and a `checkError`. `list_repositories` shows it as `setup.refused`, `get_repository` carries the result, `validate_repository` adds the same findings for its refused entries, and `reconcile_repository` names a known refusal before a dispatch that could only report it. The engine's read-mode checks gain the catalog mapping by the Component's `giantswarm.io/helmcharts` annotation (a chartless component is in the catalog with no chart to map) and the reported checks of a fresh repository whose every commit is tagged.
- Existing declarations are validated against the repositories schema alone; the creation rules (mandatory `gen.flavours`/`gen.language`, `gen.ci.generate`) apply to an added entry in `validate_repository`'s dry run only. The `declaration-refused` finding and the withheld engine checks no longer hit the 222 declared repositories the creation rules refused.
- The chart's Valkey subchart carries the restricted Pod Security Standard's security contexts (pod, container, metrics exporter) by default, so an installation enforcing PSS restricted admits it without overlay values.

- Repository bootstrap: the Go module, a health-serving binary, the Dockerfile and the Helm chart with its Valkey dependency.
- The MCP server behind muster: an OAuth 2.1 resource server (mcp-oauth) validating the Dex `id_token` muster forwards, a muster token-exchange broker client releasing the caller's GitHub grant (audience `github`), the giantswarm-align-files App identity for unattended reads, the Valkey inventory store and devctl's `pkg/reposetup` engine.
- `get_info`: the identity chain of a call (caller, grant proven with `GET /user`, App, inventory, engine, write modes).
- The write-tool framework: every write takes `dryRun` and `mode`; `mode: apply` is refused before any tool runs, `commit` is the only mode. `create_repository` with the engine's dry run.
- Chart values for OAuth (with the platform's `global.identity.*` defaults), the muster MCPServer registration, the broker client, the GitHub App and the CircleCI token, all through Secret references.
- Tests: the identity chain against fakes (`internal/e2e`), muster's scenario harness with a mocked GitHub (`tests/scenarios`), both in the `scenario-test` CI job with a Valkey service container.
- The inventory: one Valkey record per repository of the org — the declaration from the team files, the GitHub reality (GraphQL as the App: visibility, flags, last commit and last person commit, open pull requests split people/bots, issues, latest release, CODEOWNERS teams, presence of Renovate/Dependabot/CircleCI/workflows/Dockerfile/Helm), CircleCI (followed, setup workflows, last pipeline), Renovate (config, dashboard issue, last pull request), catalog entity, mapping entry, the set-up state (the engine's checks in read mode plus the last reconciler run) and the orphan score with its reasons and an adjustable stale period. Shape in `docs/inventory-record.md`.
- Refresh: a full sweep on a schedule (`inventory.sweep.interval`), one repository after a reconciler run (`POST /internal/refresh` with the run, behind `inventory.internal.existingSecret`) and on demand (`refresh_repository`); every record carries its age. The sweep pages the way the prototype learned (metadata 50 a page, histories in aliased batches of 20) and stops cleanly at a GraphQL budget floor.
- Findings the inventory shows: `declared-but-gone`, `undeclared-on-github`, `declaration-refused`, and the engine's (`default-icon`, `gen-circleci-refused`, …).
- Tools `list_repositories`, `get_repository`, `refresh_repository`, `decide_repository`; `--sweep-once` runs one sweep and prints the summary; `GITHUB_TOKEN` as a development stand-in for the App.
- devctl bumped to the engine with `pkg/reposetup/reconcile`.

### Fixed

- The creation sentence reaches the team's standup channel also for a repository the inventory has not swept since its declaration merged: the poller reads the team files again for a run behind a person's merged change when its last read predates the run — a read from before the merge had no declaration for a repository created or added, and the giving team for one transferred — and the completion hook logs why it stays silent on every way out, naming the team the artifact carries when the team files do not declare the repository (giantswarm/giantswarm-repo-manager#60).
- The chart's MCPServer registration carries `spec.timeout` from `muster.mcpServer.timeout` (default `180` seconds; the CRD allows 1–300): muster's default of 30 seconds cancelled `create_repository` in `mode: commit` during the scaffold step — the repository created, the client answered with a transport error, the scaffold and the pull request missing — and left `reconcile_repository` and `set_lifecycle` with the ask close to the limit. The write tools say that a commit may take up to a minute (giantswarm/giantswarm-repo-manager#36).
- `validate_repository` takes the same arguments as `create_repository`: `reason` (optional) is declared in its input schema and appears in the planned pull request's body (`creation.pullRequest.body`) exactly as `create_repository` writes it, so a client runs the dry run with the arguments it commits — a client that checks a call against the schema refused a dry run carrying `reason` (giantswarm/giantswarm-repo-manager#35). The two tools declare one argument list.
- The reconciler artifact poller decodes `workflowRun.id` and `workflowRun.attempt` written as JSON strings — the reconciler workflow writes both from `GITHUB_RUN_ID` and `GITHUB_RUN_ATTEMPT`, which GitHub Actions hands to the step as strings — as well as numbers; before, every artifact was rejected with `cannot unmarshal string into Go struct field .workflowRun.id of type int64` and `setup.lastRun` stayed empty for every repository. A value that is neither is still refused, naming the field (giantswarm/giantswarm-repo-manager#33).
- The server no longer exits when the inventory store is not reachable at start (it crash-looped for 20 minutes on its first production rollout while Valkey came up late): it serves at once and connects to Valkey in the background, retrying with backoff for `inventory.connectTimeout` (`INVENTORY_CONNECT_TIMEOUT`, 5m; `0` waits for ever). Until the store answers, readiness fails with the reason, the inventory tools and `/internal/*` answer `inventory unavailable` (503), and the identity tools work; the sweep schedule starts on the connected store. A Valkey lost at runtime reads the same way and is reconnected by the client (#6).



[Unreleased]: https://github.com/giantswarm/giantswarm-repo-manager/tree/main
