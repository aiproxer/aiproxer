# Project Structure

## Organization Philosophy

[Describe approach: feature-first, layered, domain-driven, etc.]

## Directory Patterns

### [Pattern Name]
**Location**: `/path/`  
**Purpose**: [What belongs here]  
**Example**: [Brief example]

### [Pattern Name]
**Location**: `/path/`  
**Purpose**: [What belongs here]  
**Example**: [Brief example]

## Naming Conventions

- **Files**: [Pattern, e.g., PascalCase, kebab-case]
- **Components**: [Pattern]
- **Functions**: [Pattern]

## Import Organization

```typescript
// Example import patterns
import { Something } from '@/path'  // Absolute
import { Local } from './local'     // Relative
```

**Path Aliases**:
- `@/`: [Maps to]

## Code Organization Principles

[Key architectural patterns and dependency rules]

## Boundary Ownership

[Record which packages own core policy, protocol adapters, plugin SDK contracts, feature extension seams, composition roots, test harnesses, and operational/security guardrails. Do not list every file.]

## Optional Hexagonal Guidance

[If useful for the project, record how domain policy, app/use-case orchestration, driving adapters, driven adapters, query seams, and composition roots map onto the existing package layout. Do not imply a repo-wide rename is required.]

---
_Document patterns, not file trees. New files following patterns shouldn't require updates_
