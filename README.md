# IronLark

IronLark is an SSH-first AI terminal assistant.

The binaries and commands are still:

- `lark`
- `lk`

IronLark is built for the moment when you SSH into a machine, hit a problem, and want AI help without leaving the terminal.

Created by Richardson Dackam ([`@richardsondx` on X](https://x.com/richardsondx)).

## Why IronLark

Use IronLark when you want AI that feels natural inside an SSH session:

- inspect a server
- read logs and config files
- search a repo
- suggest or apply small patches
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

Verify it works:

```bash
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
mkdir -p ~/.config/lark
cat > ~/.config/lark/.env <<'EOF'
OPENAI_API_KEY=your_key_here
EOF
```

Test it:

```bash
lk model
lk config test
lk "inspect this machine and tell me anything risky"
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
lk "why is nginx failing?"
```

You can also review a config change directly on the droplet:

```bash
lk edit /etc/ssh/sshd_config "disable password authentication and preserve the rest of the file"
```

## First Commands To Try

```bash
lk "why is nginx failing?"
lk inspect
lk chat
lk history
lk model
lk model gpt-5
lk edit ./README.md "rewrite the first sentence to be clearer"
```

## Commands

- `lk "task"`: one-shot inspect -> plan -> run -> verify flow
- `lk chat`: interactive shared-context session
- `lk inspect [system|repo]`: inspect the current machine or repo
- `lk edit <path> [instruction]`: patch a file with diff approval
- `lk run "<command>"`: run a shell command with policy guardrails
- `lk history [sessions|patches|checkpoints]`: show local history
- `lk undo <patch-id>`: restore a saved file backup
- `lk restore <checkpoint-id>`: restore a saved checkpoint snapshot
- `lk config init|show|set|use|test`: manage provider and approval config
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

## Local Development

Build from source:

```bash
make test
make build
```

Run the freshly built binary from the repo:

```bash
./bin/lark model
./bin/lark "hello"
```

If you already installed `lk` into `~/.local/bin`, rebuilding the repo does not update that installed binary automatically. Reinstall the rebuilt binary after code changes:

```bash
cp ./bin/lark ~/.local/bin/lark
ln -sf ~/.local/bin/lark ~/.local/bin/lk
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

## Open Source

- License: MIT
- Commands stay `lark` and `lk`
- Project name: IronLark
