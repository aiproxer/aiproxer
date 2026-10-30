<!-- Derived from the Codex memories templates (openai/codex at commit 0df4aa67781ae45a3c97befa4db48b734afe46d9), Apache-2.0 licensed; adapted for the AIProxer proxy memory feature. -->

## Proxy Memory: Output Contract

The instructions above about reading files, running commands, using `git`,
deleting files, or creating `skills/` do not apply to this call: skills are not
supported, so do not create or reference any skill content.

Return only a JSON object with the complete new contents of both files, and
nothing else around it:

{"memory_md": "<full MEMORY.md>", "memory_summary": "<full memory_summary.md>"}

`memory_summary` must follow the format rules above, including the first line
`v1`. If there is nothing to remember, return empty strings.
