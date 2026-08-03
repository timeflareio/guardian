# Planning — rules for authoring and refining plans

This directory holds design/implementation plans. A plan is a contract we refine
until it is unambiguous, *then* execute. These rules keep that discipline honest.

## Lifecycle

- Plans are named `PENDING_<NAME>_PLAN.md` (prefix `PRIORITY_` for the highest
  priority). On completion they are renamed `DONE_<NAME>_PLAN.md` and moved to
  `done/`, retained as the design rationale and decision log.
- **Plan documents live on `main`.** A plan is authored, refined and committed
  directly on `main` so it is reviewable ahead of execution. Worktrees are for
  *execution only* (see Execution) — an executing worktree receives the plan by
  forking `main`; a plan document is never created on an execution branch.
- **No changes are made until a plan is executed.** If refining reveals that
  associated edges need updating — docs, another module, a client, tests — that
  work is *added to the plan*, not done ahead of it.

## Every plan has a header

- **Priority** — `P0`–`P3`, most urgent first, with a one-line reason
  (e.g. `P1 — protocol structure, pre-testnet`).
- **Status** — one of: `refining` → `ready` → `in progress` → `done`.
  - **Status starts as `refining`** and stays there until every open question and
    uncertainty is resolved. A plan does not become `ready` (executable) with open
    items outstanding.
  - Record the date and, where relevant, the branch/PR as status advances.
- **Origin** — link the motivating issue, CHAIN_MECHANICS.md ledger item (oddity/observation/defect), review, or
  design session.
- **Components** — enumerate every surface the plan touches (modules, proto,
  clients, docs, tests). This is the blast-radius checklist for execution.

## Refining discipline

- **Open questions are explicit.** Keep an "Open questions" section; give each
  item a recommendation. When the owner rules, **fold the decision into the plan
  body** and remove it from the open list — the plan should read as the settled
  design, not a transcript.
- **A plan reads as if it never changed.** When a decision evolves, rewrite the
  affected parts *in place* so the document presents one coherent current design.
  Keep the *why* — the rationale and constraints behind a choice are important
  detail — but drop the churn: no "this supersedes X", "previously we did Y",
  "changed from Z". Evolution lives in git history, not the prose. Reading clean
  is not the same as omitting: cut the history, keep the reasoning.
- **One plan, one concern.** A plan covers a single concern; its associated edges
  (rule 1) fold in, but an *unrelated* concern gets its own plan. If refining
  splits a concern in two, split the plan (as the quorum and accept-then-distribute
  work was split).
- **State what the plan does *not* solve.** Scope boundaries and known residual
  risks are documented plainly. No overclaiming; if a change bounds a problem
  rather than closing it, say so.
- **Spec-first.** `docs/spec.md` is the protocol authority. A plan that changes
  protocol behaviour leads its implementation phases with the spec/doc update
  *before* code, and lists the docs to sweep as a deliverable in their own right.
- **Renames/removals get a cross-component sweep.** Removing or renaming a wire
  field or shared symbol carries a grep-driven audit across *all* listed
  components (the ShareIndex-removal lesson in CLAUDE.md). Confirm which
  components are clear, not just which change.
- **A new component needs an argued case.** Introducing a new module, package,
  service, binary, build target, or second implementation requires stating the
  case and getting explicit approval first (CLAUDE.md architectural minimalism).
  Default to extending what exists; where duplication is unavoidable, pin it with
  shared `testdata/vectors/` so it cannot drift.

## Execution

- **Plans are executed in a worktree that forks `main`.** The refined plan drives
  the branch; `main` is never worked on directly.
- **Run the relevant tests locally before opening a PR.** A plan reaching `done`
  means its tests pass locally first, not that they are left for CI to discover.
- **Claim the devnet before touching it.** There is only one. `~/.timeflare`,
  the installed `timeflared` and the running node are machine-global, so they
  are shared by every worktree at once — two executions verifying together
  read each other's blocks and each other's guardians, and `make dev-reset`
  wipes the state the other one is part-way through asserting against. The
  failure is worse than a clean clash: the results still *look* like results.
  Take the lock before `make dev-up`, `dev-reset`, `e2e`, `e2e-scenarios`, or
  anything else that drives the chain.
- **The lock is `chain.lock` in the main checkout's root** — never inside a
  worktree, so every worktree resolves the same one file. From anywhere in the
  repo that path is `$(dirname $(git rev-parse --path-format=absolute
  --git-common-dir))/chain.lock`; do not hard-code it, and do not put a copy
  in `.devnet/`, which is per-checkout and would defeat the point. It is
  gitignored: this is coordination between concurrent sessions, not history,
  and it must never reach a branch or a PR.
- **The lock says who holds it and what they are doing**, so anyone who finds
  it can decide whether to wait, work elsewhere, or ask:

  ```
  plan:     PENDING_<NAME>_PLAN.md
  branch:   worktree-<name>
  worktree: .claude/worktrees/<name>
  claimed:  2026-08-01T14:22Z          # absolute, UTC
  running:  make e2e-scenarios
  pid:      48213                       # the shell holding it
  ```

- **A held lock is not yours to take.** Do the work that needs no chain, or
  wait, or ask the owner — never start a second devnet, and never reset one
  you did not claim.
- **Release it the moment the run ends** — delete the file. It covers a run,
  not a task: holding it through a review, a rewrite or a long think blocks
  everyone for no reason, and re-claiming costs nothing.
- **A stale lock is reclaimed openly.** If the `pid` is gone and no
  `timeflared` is running, the holder died without cleaning up: say so, replace
  the file with your own claim, and mention it in the session. Never delete
  someone's lock silently — a lock that can vanish unannounced is not a lock.
- **Observe the PR until all status checks pass.** A raised PR is not finished
  until CI is green.

## Conventions

- **British English** throughout (see `STYLE_GUIDE.md`).
- **Absolute dates**, never relative ("July 2026", not "last month").
- **Cross-link related plans.** Where a plan depends on, hands off to, or is
  bounded by another, link it — the dependency graph between plans should be
  navigable from the prose (e.g. an ownership handoff naming the plan that owns
  the residual).
