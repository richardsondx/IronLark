# IronLark

IronLark is an SSH-first AI terminal assistant built for the moment when you SSH into a machine, hit a problem, and want AI help without leaving the terminal.

[![Image from Gyazo](https://i.gyazo.com/d9fb22c9211e51c94286f039922bbc03.gif)](https://gyazo.com/d9fb22c9211e51c94286f039922bbc03)

## Why IronLark

Use IronLark when you want AI that feels natural inside an SSH session:
- inspect a server
- read logs and config files
- search a repo
- suggest or apply small patches
- keep short-term memory across one-shot commands in the same shell
- execute obvious safe inspection work immediately
- keep explicit approval around risky actions
- work well on a remote box such as a DigitalOcean droplet

IronLark is intentionally opinionated toward terminal and server workflows, not IDE-first workflows.

IronLark is intentionally lightweight: a single Go binary that runs entirely inside the terminal, with no browser, IDE, or desktop dependencies. The policy system is focused on safe shell and file operations, and installing is just downloading the binary into `~/.local/bin` (no daemon or service required).
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

## First Commands To Try

```bash
lk "what can you help me do on this server?"
lk "summarize this machine"
lk "what's my ip?"
lk "remember that my project is in /opt/"
lk "what did we just find?"
lk --plan "debug why nginx won't start"
lk context
lk context window
lk policy list
lk inspect
lk chat
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
- `lk context`: show the active persistent context thread
- `lk context window`: show the current context-window usage and replay preview
- `lk context clear`: clear the active thread history but keep the thread record
- `lk context drop`: delete the active thread
- `lk context use <thread-id>`: pin the current working directory to a manual thread
- `lk context list`: list recent context threads
- `lk policy list`: show machine-level allow/deny rules
- `lk policy allow <action|command|path> <value>`: persist an allow rule on this machine
- `lk policy deny <action|command|path> <value>`: persist a deny rule on this machine
- `lk policy remove <id>`: remove a machine policy rule
- `lk inspect [system|repo]`: inspect the current machine or repo
- `lk edit <path> [instruction]`: patch a file with diff approval
- `lk run "<command>"`: run a shell command with policy guardrails
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

### Machine Policy

IronLark stores allow/deny rules per machine so repeat approvals can disappear for trusted commands.

```bash
lk policy list
lk policy allow command "systemctl status"
lk policy allow command "journalctl -u"
lk policy deny command "rm"
lk policy remove <rule-id>
```

Read-only actions are generally auto-approved, but sensitive paths such as `.env`, key files, and SSH material still require approval.

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
