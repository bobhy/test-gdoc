# Shared Agent Skills

This directory contains agent skills following the [Agent Skills](https://agentskills.io) standard.

Because `.claude/skills`, `.gemini/skills`, and `.github/skills` are symlinked directly to this `.agents/skills` directory, any skill added here is automatically discovered by:

- **Gemini** (Antigravity via `.agents/skills/` and Gemini CLI via `.gemini/skills/`)
- **Claude Code** (via `.claude/skills/`)
- **GitHub Copilot** (via `.github/skills/` and `.agents/skills/`)

## Adding a skill

1. Create a subdirectory using lowercase kebab-case (e.g. `.agents/skills/my-skill/`).
2. Add a `SKILL.md` file with required YAML frontmatter (`name` and `description`):
   ```markdown
   ---
   name: my-skill
   description: Short description of what this skill does and when to activate it.
   ---

   # Skill instructions
   ```
3. Optionally include `scripts/`, `references/`, or `assets/` subdirectories inside the skill folder.
