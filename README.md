# IronLark

IronLark is an SSH-first AI terminal operator built for the moment when you SSH into a machine, hit a problem, and want an agent that can inspect, fix, watch, and report back without leaving the terminal.

[![Image from Gyazo](https://i.gyazo.com/d9fb22c9211e51c94286f039922bbc03.gif)](https://gyazo.com/d9fb22c9211e51c94286f039922bbc03)

## Why IronLark

Use IronLark when you want an agent that feels native inside an SSH session and can take responsibility for machine outcomes:
- inspect a server, logs, configs, processes, ports, and repos
- keep persistent shell-scoped context across one-shot commands and `lk agent`
- recover a service in the background and come back only when it is healthy or clearly blocked
- watch a service continuously, capture evidence, and handle obvious restart-only incidents automatically
- keep a separate operational memory of watchers, recoveries, incidents, and audit trails
- give you an emergency control plane with `lk ps` to inspect, stop, or kill live background agents
- execute obvious safe inspection work immediately while preserving explicit approval boundaries for risky actions

IronLark is intentionally opinionated toward terminal and server workflows, not IDE-first workflows.

IronLark is intentionally lightweight: a single Go binary that runs entirely inside the terminal, with no browser, IDE, or desktop dependencies. The policy system is focused on safe shell and file operations, and installing is just downloading the binary into `~/.local/bin` (no daemon or service required).

## What Makes IronLark Different

IronLark is built around terminal-native operational workflows, not just prompt-response assistance:

- **SSH-first operator experience**: work directly on the remote box where the problem lives
- **Persistent machine memory**: graph snapshots, recent changes, incidents, and background task state stay local to the machine
- **Background ops runtime**: watchers and recoveries run outside your current chat turn
- **Outcome-oriented workflows**: ask IronLark to restore a service or keep it healthy, not just suggest the next command
- **Emergency controls**: `lk ps` shows live IronLark processes so you can stop token bleed or kill a stuck run fast
- **Readable audit trail**: evidence bundles, progress logs, incident summaries, and command history remain inspectable after the fact
## Quick Start

### Local machine

Install IronLark:

```bash
curl -fsSL https://raw.githubusercontent.com/richardsondx/IronLark/main/install.sh | sh
```

Add your API key:

```bash
mkdir -p ~/.config/lark
cat > ~/.config/lark/.env <<'EOF'
OPENAI_API_KEY=your_key_here
EOF
```

or

```bash
export OPENAI_API_KEY=your_key_here
```

Verify it works:

```bash
lk init
lk version
lk model
lk config test
lk "hello"
```

### Remote server over SSH

SSH into the server:

```bash
ssh root@your-server-ip
```

Install IronLark on that server:

```bash
curl -fsSL https://raw.githubusercontent.com/richardsondx/IronLark/main/install.sh | sh
```

If `~/.local/bin` is not on the remote `PATH`, add it:

```bash
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.bashrc
source ~/.bashrc
```

Add your API key on the server:

```bash
lk init
```

Test it:

```bash
lk version
lk model
lk config test
lk "what can you help me do on this server?"
lk agent
```

Try a file edit:

```bash
lk edit /etc/nginx/nginx.conf "enable gzip and keep the current file structure"
```

Before IronLark applies a file change, it shows the proposed unified diff in the terminal. Added lines are highlighted in green, removed lines are highlighted in red, and diff hunk headers are highlighted so you can review the patch before approving it.

### Example: DigitalOcean droplet

On a fresh Ubuntu droplet, this is usually enough:

```bash
ssh root@your-droplet-ip
curl -fsSL https://raw.githubusercontent.com/richardsondx/IronLark/main/install.sh | sh
mkdir -p ~/.config/lark
cat > ~/.config/lark/.env <<'EOF'
OPENAI_API_KEY=your_key_here
EOF
export PATH="$HOME/.local/bin:$PATH"
lk config test
lk "what can you help me do on this server?"
```

You can also review a config change directly on the droplet:

```bash
lk edit /etc/ssh/sshd_config "disable password authentication and preserve the rest of the file"
```

## Operator Workflows

IronLark is strongest when you use it for delegated terminal work, not just one-shot questions.

### Recover a service

Start an interactive session:

```bash
lk agent
```

Then ask in plain English or use the explicit command:

```text
/recover openclaw
```

or

```bash
lk recover "restore openclaw and keep going until it is stable"
```

Recovery runs are durable background jobs. They keep progress, timeline events, evidence, and a progress log under the local Lark data directory.

### Watch a service

```text
/watch openclaw
```

or

```bash
lk watch openclaw
```

Watchers use the graph plus active probes to monitor a service, capture evidence first, and apply a narrow restart-only remediation only when the cause looks obvious enough.

### Inspect background ops work

```bash
lk ps
lk watch list
lk watch report openclaw
lk recover list
lk recover report <run-id>
```

`lk ps` is the emergency control plane for live IronLark work. It shows active watchers, recoveries, and agent sessions with pid, state, age, last activity, and token usage.

## First Commands To Try

```bash
lk "what can you help me do on this server?"
lk "summarize this machine"
lk "what's my ip?"
lk "remember that my project is in /opt/"
lk "what did we just find?"
lk --plan "debug why nginx won't start"
lk agent
lk watch openclaw
lk recover "restore openclaw and keep going until it is healthy"
lk ps
lk watch list
lk recover list
lk context
lk context window
lk policy list
lk inspect
lk history
lk version
lk update
lk model
lk model gpt-4.1-mini
lk edit ./README.md "rewrite the first sentence to be clearer"
```

## Commands

- `lk "task"`: execute-first one-shot flow with persistent thread context by default
- `lk --plan "task"`: show a visible plan before execution
- `lk plan "task"`: explicit plan-first mode
- `lk chat`: interactive shared-context session that also writes to the current thread store
- `lk agent`: interactive SSH-first operator session with slash-command shortcuts for background ops
- `lk context`: show the active persistent context thread
- `lk context window`: show the current context-window usage and replay preview
- `lk context clear`: clear the active thread history but keep the thread record
- `lk context drop`: delete the active thread
- `lk context use <thread-id>`: pin the current working directory to a manual thread
- `lk context list`: list recent context threads
- `lk policy list`: show machine-level auto-accept settings and allow/deny rules
- `lk policy allow <action|command|path> <value>`: persist an allow rule on this machine
- `lk policy deny <action|command|path> <value>`: persist a deny rule on this machine
- `lk policy auto-accept <off|low|medium|high>`: set a machine-level risk threshold for automatic approval
- `lk policy remove <id>`: remove a machine policy rule
- `lk inspect [system|repo]`: inspect the current machine or repo
- `lk edit <path> [instruction]`: patch a file with diff approval
- `lk run "<command>"`: run a shell command with policy guardrails
- `lk watch <query>`: start a background watcher for a service, container, or app
- `lk watch list|status|report|stop`: inspect and manage watchers
- `lk recover <goal>`: start a background recovery run for a target outcome
- `lk recover list|status|report|stop`: inspect and manage recovery runs
- `lk ps`: list active IronLark processes across agent sessions, watchers, and recovery runs
- `lk ps stop <id|pid>`: gracefully stop a background IronLark process
- `lk ps kill <id|pid>`: force kill a background IronLark process
- `lk history [sessions|patches|checkpoints]`: show local history
- `lk undo <patch-id>`: restore a saved file backup
- `lk restore <checkpoint-id>`: restore a saved checkpoint snapshot
- `lk config init|show|set|use|test`: manage provider and approval config
- `lk init`: guided setup for OpenAI auth, defaults, and PATH
- `lk version`: show the installed version
- `lk update`: update to the latest GitHub release
- `lk models list|current|set <model>`: list, show, or set the default model
- `lk model [name]`: shortcut to show the current model or set a new one

## Configuration

IronLark reads configuration from:

- `~/.config/lark/config.yaml`
- `./.lark.yaml`

Environment variables can be loaded from:

- `~/.config/lark/.env`
- `./.env`

Existing shell environment variables take precedence over values from env files.

### Execute-First By Default

Normal one-shot `lk "..."` usage is execute-first. IronLark plans internally, but for simple low-risk inspection work it runs the minimum safe actions immediately instead of showing a full proposed-action block first.

- safe reads and low-risk inspection commands run immediately
- risky commands and edits stop at the next approval boundary
- `--plan` or `lk plan` switches back to visible plan-first review
- persistent machine policy rules can suppress repeat approvals for trusted actions

### Persistent Thread Context

By default, one-shot `lk "..."` commands reuse a lightweight local thread so follow-up prompts in the same shell and directory keep their context.

- thread state is stored locally under the Lark data directory
- context is scoped to the current shell when possible, with a cwd fallback
- older turns are compacted into a rolling summary as the context window fills
- IronLark warns when the estimated context window is getting close to full

Useful controls:

```bash
lk context
lk context window
lk context clear
lk context drop
lk --no-context "run this statelessly"
lk --new-thread "start a fresh thread for this run"
lk --thread incident-123 "continue a specific thread"
lk --narrated-progress on   # show status/timeline updates in `lk agent`
```

Relevant config keys in `~/.config/lark/config.yaml`:

```yaml
interaction_mode: execute-first
ui:
  narrated_progress: false
tools:
  soft_turns: 5
  max_turns: 12
thread:
  enabled: true
  scope: auto-shell
  max_tokens: 12000
  warn_at_ratio: 0.8
  recent_turns: 8
  max_result_chars: 1200
  auto_compact: true
```

Set `ui.narrated_progress: true` to enable the narrated progress timeline in the interactive `lk agent` TUI. Plain one-shot output and JSON mode keep the existing behavior.
`tools.soft_turns` is the normal execution budget. `tools.max_turns` is the hard cap, and IronLark now stops explicitly as incomplete if it reaches that cap before a `finish` action.

### Background Ops Runtime

Watchers and recovery runs are stored separately from the main chat transcript so they can continue working while you do something else.

- watchers live under the local ops data directory
- recovery runs keep `spec.json`, `state.json`, `timeline.jsonl`, `progress.md`, and evidence bundles
- incidents are persisted so you can ask later what happened
- `lk agent` shows a compact ops summary in the header when background work exists
- `/ops`, `/watch <query>`, and `/recover <goal>` are available directly inside `lk agent`

Typical flow:

```text
lk agent
/watch openclaw
/ops
```

Then later:

```text
what happened overnight?
```

IronLark can use operational memory to answer from recent watchers, recoveries, and incidents.

### Machine Policy

IronLark stores allow/deny rules per machine so repeat approvals can disappear for trusted commands. You can also set a machine-level auto-accept threshold that approves future actions at or below a risk level. Deny rules still win.

```bash
lk policy list
lk policy auto-accept medium
lk policy allow command "systemctl status"
lk policy allow command "journalctl -u"
lk policy deny command "rm"
lk policy remove <rule-id>
```

Interactive approval prompts also expose:

- `Allow once`
- `Always allow on this machine`
- `Auto-accept (<=LOW|MEDIUM|HIGH)`
- `Deny once`
- `Cancel`

When the auto-accept row is selected in an interactive terminal, `Tab` cycles the threshold. `HIGH` auto-accepts everything, `MEDIUM` auto-accepts low and medium risk actions, and `LOW` auto-accepts only low risk actions.

Read-only actions are generally auto-approved, but sensitive paths such as `.env`, key files, and SSH material still require approval unless your machine auto-accept threshold explicitly covers their risk level.

## Review Before File Changes

When IronLark proposes a file edit, it prints a unified diff before asking for approval.

- added lines are highlighted in green
- removed lines are highlighted in red
- diff hunk headers are highlighted for easier review

This makes it easier to understand exactly what will change before you type `y`.

## What IronLark Can Do Today

IronLark can currently use terminal-native tools for:

- exact file search
- semantic-style local retrieval
- local rule discovery
- web search
- file reads
- guarded edits
- inline checkpoints before edits
- command execution with policy checks
- persistent short-term thread context across one-shot commands
- persistent graph-backed machine memory
- background watcher and recovery runtimes
- incident evidence capture and reporting
- process-level emergency control through `lk ps`

With the default `openai` provider, planning and web search run through the OpenAI Responses API. Other `openai-compatible` providers continue using the local web-search fallback.

## Local Development

Build from source:

```bash
make test
make build
make install
```

Run the freshly built binary from the repo:

```bash
./bin/lark model
./bin/lark "hello"
```

If you already installed `lk` into `~/.local/bin`, use one command to rebuild and reinstall it:

```bash
make install
```

Verify the installed binary is the updated one:

```bash
lk model
lk models current
```

To test the diff preview locally:

```bash
make build
./bin/lark edit ./README.md "change the first sentence to say IronLark is an SSH-first AI terminal assistant"
```

You should see a proposed diff in the terminal before approval. If your terminal supports ANSI colors, additions will be green and deletions will be red. To disable colors:

```bash
NO_COLOR=1 ./bin/lark edit ./README.md "change the first sentence"
```

## Notes

- Credentials are read from environment variables.
- For SSH-heavy workflows, forwarding env vars is still useful if you prefer `SendEnv` and `AcceptEnv`.
- The current GitHub install script installs released binaries. If you want unreleased local changes on a remote server, copy your freshly built binary there manually or publish a release first.


## Author 

Created by Richardson Dackam ([`@richardsondx` on X](https://x.com/richardsondx)).

## Open Source

- License: MIT
- Commands stay `lark` and `lk`
- Project name: IronLark
