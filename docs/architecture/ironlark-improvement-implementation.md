# Iron-Lark Runtime Upgrade

This document records the implementation work done from the improvement plan without modifying the plan file itself. It is intentionally focused on executable architecture, not just long-term ideas.

## 1. Baseline Gap Analysis

Claude Code's strongest architectural advantage is not any single feature. It is the combination of a typed tool lifecycle, durable task model, transcript-plus-memory compaction, and explicit permission boundaries. Iron-Lark was already strong at SSH-first ops workflows, machine graph context, persistent local threads, and background watch/recover flows, but the core task loop still relied on a single strict JSON blob per turn and a large engine/executor switch tree.

The highest-value gaps were:

- local tools were represented mostly as prompt-described action types rather than a code-level runtime contract
- durable background work existed, but inline tool execution and background ops did not share a unified task abstraction
- session persistence stored results, but not extracted memories for later compaction
- safety leaned heavily on command heuristics and needed stronger dangerous-pattern detection
- multi-agent and MCP-style extensibility were future directions, not runtime primitives

## 2. Engine Decomposition Direction

`internal/engine/engine.go` remains the orchestrator, but the refactor direction is now clearer:

- `Engine` should own turn orchestration, context selection, continuation policy, and renderer events
- execution policy should stay at the engine boundary
- action execution should move behind a tool runtime contract
- task recording should happen in the executor/runtime layer
- memory extraction should happen after a turn completes and before session persistence

This change set implements the first practical decomposition seam by moving the first batch of action kinds behind a typed runtime while keeping current behavior stable.

## 3. Typed Tool Runtime

The first slice is implemented in `internal/toolruntime` and `internal/executor/tool_runtime.go`.

What changed:

- a typed registry now exists in `internal/toolruntime/runtime.go`
- the executor now auto-initializes a local runtime
- four core actions are runtime-backed:
  - `run_shell`
  - `read_files`
  - `search_files`
  - `edit_file`
- runtime-backed executions now stamp action results with:
  - `handler`
  - `task_id`

This is intentionally incremental. The rest of the actions still use the legacy executor path, which keeps the system stable while providing a migration seam for the remaining tools.

## 4. Durable Task Model

The first version of a unified task record format now lives in `internal/taskruntime/store.go`.

Current scope:

- each runtime-backed action creates a durable task record
- tasks capture:
  - task kind
  - state
  - action id and action type
  - handler name
  - target
  - summary or error
  - background run linkage
  - start and finish timestamps
- task records are stored under the new data path configured at `Paths.TaskRunsDir`

This does not replace the richer ops runtime yet. It creates the common format needed so inline actions, shell promotions, watchers, recoveries, and future subagents can converge later.

## 5. Memory and Session Persistence

The first compact memory extractor now lives in `internal/memory/extract.go`.

What changed:

- completed sessions now store `memories` in `internal/sessions/store.go`
- both one-shot task runs and chat turns extract reusable memory facts before saving the session
- extracted memories favor compact operational facts over narrative replay

This gives Iron-Lark a bridge from rolling thread summaries toward transcript-plus-memory compaction.

## 6. Safety Upgrades

The policy classifier now blocks a small but important class of dangerous shell patterns:

- `curl` or `wget` piped directly to `sh` or `bash`
- shell-profile mutation commands targeting files like `.bashrc` and `.zshrc`

The runtime-backed file handlers also normalize paths and refuse to operate directly on symlink targets. That is not a full sandbox, but it meaningfully tightens the most important edit/read/search path.

## 7. Multi-Agent and MCP Sequencing

These are still intentionally sequenced after the runtime foundations:

- next, extend the typed runtime to all remaining action kinds
- then, converge inline action tasks and ops background runs onto the same task model
- then, add transcript compaction and structured memory extraction beyond the current lightweight facts
- only after those are stable, add bounded planner-worker subagents for repo exploration, triage, and verification
- after subagent boundaries are reliable, add an ops-focused MCP adapter layer for external observability and cloud tools

The key rule is unchanged: Iron-Lark should only add multi-agent complexity when it directly improves SSH/server outcomes.
