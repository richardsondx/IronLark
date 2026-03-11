# Benchmark Guide

This guide collects the terminal-bench commands and notes so the main README stays focused on product usage.

## Quick Smoke Test (local dataset)

From the repo root:

```bash
./scripts/run_tb_ironlark.sh smoke
```

Uses the local task at `tasks/ironlark-smoke` to validate the adapter plus IronLark execution path.

## Core Dataset (published)

```bash
./scripts/run_tb_ironlark.sh core
```

The wrapper expands to terminal-bench with:

- `--agent-import-path python_adapter.ironlark_adapter:IronLarkAgent`
- `--model openai/gpt-4.1-mini`
- `--agent-kwarg install_mode=source`
- `--agent-kwarg approval=agent`

You can override those with environment variables:

```bash
TB_MODEL=openai/gpt-5-mini TB_INSTALL_MODE=release ./scripts/run_tb_ironlark.sh smoke
```

## Direct `tb run` Example

```bash
tb run \
  --dataset terminal-bench-core==0.1.1 \
  --agent-import-path python_adapter.ironlark_adapter:IronLarkAgent \
  --model openai/gpt-4.1-mini \
  --agent-kwarg install_mode=source \
  --agent-kwarg approval=agent
```

## Minimal Core Subset (first 10 tasks)

```bash
tb run \
  --dataset terminal-bench-core==0.1.1 \
  --n-tasks 10 \
  --agent-import-path python_adapter.ironlark_adapter:IronLarkAgent \
  --model openai/gpt-5-codex \
  --agent-kwarg install_mode=source \
  --agent-kwarg approval=agent
```

## Notes

- `terminal-bench-core=head` currently resolves to a registry entry whose `dataset_path` points to `./tasks`, but that path is missing in the upstream checkout terminal-bench downloads, so `head` fails with `FileNotFoundError`.
- If a run shows `provider is not configured or API key is unavailable`, ensure `OPENAI_API_KEY` is available inside the benchmark environment.
