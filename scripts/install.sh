#!/usr/bin/env bash
# Put the scratchpad CLI on PATH and install the skill that teaches agents to
# drive it. There is no MCP server: the CLI is the only agent interface.
# Usage: install.sh [cli|skill|drop-mcp|all]
set -euo pipefail

TARGET="${1:-all}"
REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

install_cli() {
  mkdir -p "$HOME/.local/bin"
  ln -sf "$REPO_DIR/bin/scratchpad" "$HOME/.local/bin/scratchpad"
  echo "cli: symlinked to ~/.local/bin/scratchpad"
}

install_skill() {
  # Agent Skills standard (agentskills.io): one SKILL.md serves every agent
  # that can run bash — Claude Code, pi, and anything else reading this dir.
  for dst in "$HOME/.claude/skills/scratchpad" "$HOME/.pi/agent/skills/scratchpad"; do
    mkdir -p "$dst"
    cp "$REPO_DIR/skill/SKILL.md" "$dst/SKILL.md"
    echo "skill: installed at $dst/SKILL.md"
  done
  rm -f "$HOME/.pi/agent/extensions/scratchpad.ts" # superseded by skill+CLI
}

# drop_mcp removes registrations left by older versions of this script, back
# when scratchpad shipped a /scratchpad-mcp binary. Safe to re-run.
drop_mcp() {
  if command -v claude >/dev/null; then
    claude mcp remove -s user scratchpad >/dev/null 2>&1 || true
    claude mcp remove -s local scratchpad >/dev/null 2>&1 || true
    echo "mcp: removed any claude registration"
  fi
  local oc="$HOME/.config/opencode/opencode.json"
  if [ -f "$oc" ]; then
    CFG="$oc" python3 - <<'EOF'
import json, os
p = os.environ["CFG"]
with open(p) as f:
    cfg = json.load(f)
block = cfg.get("mcp") or {}
# 1.x keeps servers directly under "mcp"; 2.x nests them under "mcp.servers".
holders = [block, block.get("servers") or {}]
gone = any(h.pop("scratchpad", None) is not None for h in holders if isinstance(h, dict))
if gone:
    with open(p, "w") as f:
        json.dump(cfg, f, indent=2)
    print(f"mcp: removed from {p}")
EOF
  fi
  local goose="$HOME/.config/goose/config.yaml"
  local py="python3"
  if ! python3 -c 'import yaml' 2>/dev/null && command -v uv >/dev/null; then
    py="uv run --quiet --with pyyaml python3"
  fi
  if [ -f "$goose" ] && $py -c 'import yaml' 2>/dev/null; then
    CFG="$goose" $py - <<'EOF'
import os, yaml
p = os.environ["CFG"]
with open(p) as f:
    cfg = yaml.safe_load(f) or {}
if (cfg.get("extensions") or {}).pop("scratchpad", None) is not None:
    with open(p, "w") as f:
        yaml.safe_dump(cfg, f, sort_keys=False)
    print(f"mcp: removed from {p}")
EOF
  fi
}

case "$TARGET" in
  cli)      install_cli ;;
  skill)    install_skill ;;
  drop-mcp) drop_mcp ;;
  all)      install_cli; install_skill; drop_mcp ;;
  *) echo "usage: $0 [cli|skill|drop-mcp|all]" >&2; exit 2 ;;
esac
