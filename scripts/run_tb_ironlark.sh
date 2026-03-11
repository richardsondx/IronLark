#!/usr/bin/env bash
set -euo pipefail

MODE="${1:-smoke}"
shift || true

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export PYTHONPATH="$ROOT_DIR${PYTHONPATH:+:$PYTHONPATH}"

COMMON_ARGS=(
  --agent-import-path python_adapter.ironlark_adapter:IronLarkAgent
  --model "${TB_MODEL:-openai/gpt-4.1-mini}"
  --agent-kwarg "approval=${TB_APPROVAL:-agent}"
  --agent-kwarg "install_mode=${TB_INSTALL_MODE:-source}"
)

case "$MODE" in
  smoke)
    exec tb run \
      --dataset-path "$ROOT_DIR/tasks" \
      --task-id ironlark-smoke \
      "${COMMON_ARGS[@]}" \
      "$@"
    ;;
  core)
    exec tb run \
      --dataset "terminal-bench-core==0.1.1" \
      "${COMMON_ARGS[@]}" \
      "$@"
    ;;
  *)
    echo "Usage: $0 [smoke|core] [extra tb args...]" >&2
    exit 1
    ;;
esac
