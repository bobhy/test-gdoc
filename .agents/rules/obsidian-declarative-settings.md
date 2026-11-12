# Obsidian 1.13+ Declarative Settings API Notes

Source: `obsidianmd/obsidian-developer-docs` (`en/Plugins/User interface/Settings.md` and `en/Plugins/Guides/Migrate to declarative settings.md`) and `eslint-plugin-obsidianmd` (`settings-tab/*` rules).

## Overview

Starting in **Obsidian 1.13.0**, `PluginSettingTab` supports a declarative settings API via `getSettingDefinitions()`. Obsidian uses these definitions to automatically handle:
- UI rendering
- Reading/writing `this.plugin.settings[key]` and calling `this.plugin.saveData()`
- Input validation (`validate`)
- Global settings search indexing (runs once on tab registration and on every `this.update()`)

## Migration Paths & ESLint Rules (`eslint-plugin-obsidianmd`)

| Path | `minAppVersion` | Implementation | ESLint Rules Triggered |
| :--- | :--- | :--- | :--- |
| **Path A (1.13+ only)** | `>= 1.13.0` | Implement `getSettingDefinitions()` only; delete `display()`. | `no-deprecated-display` flags leftover `display()` when `minAppVersion >= 1.13.0`. |
| **Path B (Dual support)** | `< 1.13.0` | Implement both `getSettingDefinitions()` (used by 1.13+) and `display()` (fallback on `< 1.13.0`). | `prefer-setting-definitions` warns if `getSettingDefinitions()` is missing; `require-display` errors if `display()` is missing when `minAppVersion < 1.13.0`. |

## Definition Shapes

Each item returned by `getSettingDefinitions()` can be:
1. **Setting item**: `{ name: string, desc?: string | DocumentFragment, visible?: boolean | (() => boolean), searchable?: boolean, aliases?: string[], control?: ..., render?: ..., action?: ... }`
   - `control`, `render`, and `action` are **mutually exclusive** on a single definition.
2. **Group (`type: 'group'`)**: `{ type: 'group', heading: string, items: SettingDefinition[], visible?: ..., cls?: string }`
3. **List (`type: 'list'`)**: `{ type: 'list', heading?: string, emptyState?: string, addItem?: { name: string, action: () => void }, onReorder?: (oldIndex: number, newIndex: number) => void, onDelete?: (idx: number) => void, items: ... }`
4. **Sub-page (`type: 'page'`)**: `{ type: 'page', name: string, desc?: string, items?: ... | page?: () => SettingPage }`

## Control Types (`control: { type, key, ... }`)

Every `control` binds `key` to `this.plugin.settings[key]`, auto-saves via `saveData()`, and accepts optional `defaultValue`, `disabled?: boolean | (() => boolean)`, and `validate?: (value) => string | void | Promise<string | void>`:

- `toggle`: `{ type: 'toggle', key }` (`boolean`)
- `text`: `{ type: 'text', key, placeholder?: string }` (`string`)
- `textarea`: `{ type: 'textarea', key, placeholder?: string, rows?: number }` (`string`)
- `number`: `{ type: 'number', key, min?: number, max?: number, step?: number, placeholder?: string }` (`number`)
- `slider`: `{ type: 'slider', key, min: number, max: number, step: number }` (`number` — `min`, `max`, `step` are all required)
- `dropdown`: `{ type: 'dropdown', key, options: Record<string, string>, defaultValue?: string }` (`string`)
- `file`: `{ type: 'file', key, filter?: (file: TFile) => boolean, placeholder?: string }` (`string` path)
- `folder`: `{ type: 'folder', key, filter?: (folder: TFolder) => boolean, includeRoot?: boolean, placeholder?: string }` (`string` path)
- `color`: `{ type: 'color', key }` (`string` hex)

## `render` and `action`

- **`render: (setting: Setting) => void | (() => void)`**:
  - Gives full imperative access to the `Setting` row (e.g., for a button inside a setting row via `setting.addButton(...)`, custom suggesters, or side effects on change).
  - Does **not** auto-save when mutating settings; call `await this.plugin.saveData(this.plugin.settings)` manually.
  - May return a cleanup function `() => void` for observers/timers.
- **`action: (index: number) => void`**:
  - Makes the row itself clickable (commonly used in `type: 'list'` items or sub-page action rows).

## Key Constraints & Style Rules

- Keep `getSettingDefinitions()` fast and side-effect-free (no disk I/O or network calls), as it runs at registration time for search indexing.
- Call `this.update()` (not `this.display()`) to refresh a declarative settings tab on Obsidian 1.13+ (`obsidianmd/settings-tab/prefer-update-over-display`).
- Use sentence case for all `name`, `desc`, `heading`, and `placeholder` strings (`obsidianmd/ui/sentence-case`).
- Do not add a top-level heading repeating the plugin name or "General".
