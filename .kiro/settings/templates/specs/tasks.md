# Implementation Plan

## Task Format Template

Use whichever pattern fits the work breakdown:

### Major task only
- [ ] {{NUMBER}}. {{TASK_DESCRIPTION}}{{PARALLEL_MARK}}
  - {{DETAIL_ITEM_1}} *(Include details only when needed. If the task stands alone, omit bullet items.)*
  - _Requirements: {{REQUIREMENT_IDS}}_

### Major + Sub-task structure
- [ ] {{MAJOR_NUMBER}}. {{MAJOR_TASK_SUMMARY}}
- [ ] {{MAJOR_NUMBER}}.{{SUB_NUMBER}} {{SUB_TASK_DESCRIPTION}}{{SUB_PARALLEL_MARK}}
  - {{DETAIL_ITEM_1}}
  - {{DETAIL_ITEM_2}}
  - {{OBSERVABLE_COMPLETION_ITEM}} *(At least one detail item should state the observable completion condition for this task.)*
  - _Requirements: {{REQUIREMENT_IDS}}_ *(IDs only; do not add descriptions or parentheses.)*
  - _Boundary: {{CORE_OR_PLUGIN_OR_SDK_OR_DOCS}}_
  - _Depends: {{TASK_OR_SPEC_DEPENDENCIES}}_
  - _Validation: {{TEST_OR_CHECK_COMMANDS}}_

> **Parallel marker**: Append ` (P)` only to tasks that can be executed in parallel. Omit the marker when running in `--sequential` mode.
>
> **Budget**: At most 12 leaf tasks (`.kiro/steering/delivery.md`). Each task includes the tests that prove it; there are no standalone test, certification, or documentation-contract tasks.
>
> **Boundary annotations**: For the AIProxer repo, use `_Boundary:_` for core/runtime, frontend plugin, backend plugin, feature plugin, SDK/public contract, config/wiring, docs, or tests. Use `_Validation:_` to name the focused command that proves the task.
> If the task uses a hexagonal split, name the owner precisely (`domain policy`, `app orchestration`, `driving adapter`, `driven adapter`, `composition root`, `query seam`) instead of adding generic layer work.
