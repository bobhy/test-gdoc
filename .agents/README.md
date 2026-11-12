# Shared Coding Agent Configuration

This directory (`.agents/`) and the root [`AGENTS.md`](../AGENTS.md) file are the **single source of truth** for coding-agent instructions, skills, and rules in this repository.

## Supported agents and discovery paths

All three target coding agents—**Gemini** (Antigravity and Gemini CLI), **Claude Code**, and **GitHub Copilot**—discover the same files via repository symlinks:

| Resource | Canonical path | Gemini (Antigravity / CLI) | Claude Code | GitHub Copilot |
| :--- | :--- | :--- | :--- | :--- |
| Root instructions | [`AGENTS.md`](../AGENTS.md) | `AGENTS.md` & `GEMINI.md` → `AGENTS.md` | `CLAUDE.md` → `AGENTS.md` | `AGENTS.md` & `.github/copilot-instructions.md` → `../AGENTS.md` |
| Shared skills | [`.agents/skills/`](./skills/) | `.agents/skills/` & `.gemini/skills` → `../.agents/skills` | `.claude/skills` → `../.agents/skills` | `.agents/skills/` & `.github/skills` → `../.agents/skills` |
| Shared rules | [`.agents/rules/`](./rules/) | `.agents/rules/*.md` | `.claude/rules` → `../.agents/rules` | Included via root/scoped instructions |

## Guidelines

1. **Edit canonical files only**: Update `AGENTS.md`, `.agents/skills/<name>/SKILL.md`, or `.agents/rules/<name>.md` directly rather than replacing any symlink with a standalone file.
2. **Adding a skill**: Create `.agents/skills/<skill-name>/SKILL.md` with YAML frontmatter (`name` matching `<skill-name>` and a non-empty `description`). Because `.claude/skills`, `.gemini/skills`, and `.github/skills` are directory symlinks to `.agents/skills`, new skills are immediately available to all three agents without creating per-skill symlinks.
3. **Adding a modular rule**: Create `.agents/rules/<rule-name>.md`. Both Antigravity (`.agents/rules/*.md`) and Claude Code (`.claude/rules/*.md`) load `.md` files from this directory.
