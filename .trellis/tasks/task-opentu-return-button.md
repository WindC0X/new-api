# Task: Add "Return to Console" Button in Opentu

**Created:** 2026-06-08  
**Status:** TODO  
**Priority:** Medium  
**Assignee:** Codex (external handoff)

---

## Context

### Project Overview
- **new-api**: Go-based API management console (main application)
- **opentu**: Standalone React/TypeScript canvas workspace application
- **Integration**: opentu is embedded at `/creative/` route in new-api via Go embed

### Current State (Phase 0.5 Completed)
✅ opentu successfully embedded in new-api at `/creative/`  
✅ Creative Workspace menu item added to new-api sidebar  
✅ Navigation from new-api → opentu works correctly  
❌ No way to return from opentu → new-api (this task)

---

## Objective

Add a "返回控制台" (Return to Console) button in opentu that:
1. **Only appears** when opentu is embedded in new-api (not standalone)
2. **Navigates back** to new-api dashboard when clicked
3. **Respects** opentu's existing UI/UX design patterns

---

## Technical Requirements

### 1. Environment Detection

Opentu needs to detect if it's running embedded in new-api:

**Detection Strategy:**
```typescript
// Check if running at /creative/ path
const isEmbedded = window.location.pathname.startsWith('/creative/');

// Or use environment variable (set during build)
const PARENT_APP_URL = import.meta.env.VITE_PARENT_APP_URL;
```

**Build Configuration:**
- opentu is built with `VITE_BASE_URL=/creative/` (already configured)
- May need additional `VITE_PARENT_APP_URL=http://localhost:3009` for return navigation

### 2. UI Implementation

**Button Placement:**
- Top-left or top-right corner of opentu canvas
- Non-intrusive, small icon/text button
- Consistent with opentu's design system

**Button Behavior:**
```typescript
const handleReturnToConsole = () => {
  // Navigate to new-api dashboard
  window.location.href = '/dashboard';
  // Or use parent URL from env: window.location.href = PARENT_APP_URL;
};
```

**Component Structure:**
```typescript
// Suggested location: apps/web/src/components/ReturnButton.tsx
export function ReturnButton() {
  const isEmbedded = checkIfEmbedded();
  
  if (!isEmbedded) return null;
  
  return (
    <button onClick={handleReturnToConsole}>
      ← 返回控制台
    </button>
  );
}
```

### 3. Integration Points

**Files to Modify:**

1. **`apps/web/src/app/app.tsx`** (Main app component)
   - Import and render `<ReturnButton />`
   - Place in appropriate layout slot

2. **`apps/web/vite.config.ts`** (Build configuration)
   - Ensure `VITE_BASE_URL` and `VITE_PARENT_APP_URL` are configurable
   - Current config already has: `base: process.env.VITE_BASE_URL || './'`

3. **New file: `apps/web/src/components/ReturnButton.tsx`**
   - Create button component
   - Handle environment detection
   - Implement navigation logic

---

## Implementation Steps

### Step 1: Create ReturnButton Component
```bash
cd /mnt/f/code/project/opentu/apps/web/src/components
# Create ReturnButton.tsx with detection + UI
```

### Step 2: Integrate into App
```typescript
// In apps/web/src/app/app.tsx
import { ReturnButton } from '../components/ReturnButton';

// Add to render tree (top-level, absolute positioned)
<div className="app-root">
  <ReturnButton />
  {/* existing app content */}
</div>
```

### Step 3: Rebuild opentu
```bash
cd /mnt/f/code/project/opentu
export VITE_BASE_URL=/creative/
export VITE_PARENT_APP_URL=http://localhost:3009
cd apps/web
pnpm run build
```

### Step 4: Deploy to new-api
```bash
# Copy rebuilt dist to new-api
rsync -av --delete \
  /mnt/f/code/project/opentu/dist/apps/web/ \
  /mnt/f/code/project/new-api/web/creative/dist/

# Rebuild new-api binary
cd /mnt/f/code/project/new-api
go build -o /tmp/new-api-with-return-button .

# Restart service
tmux kill-session -t newapi-demo
tmux new-session -d -s newapi-demo \
  "PORT=3009 SESSION_SECRET=demo /tmp/new-api-with-return-button"
```

---

## Testing Checklist

- [ ] Button **appears** when accessing `http://localhost:3009/creative/`
- [ ] Button **does NOT appear** when accessing opentu standalone
- [ ] Clicking button navigates to `http://localhost:3009/dashboard`
- [ ] Button styling matches opentu UI design
- [ ] Button does not obstruct canvas functionality
- [ ] Works across different screen sizes (responsive)

---

## Key Files Reference

### new-api (Go Backend)
```
/mnt/f/code/project/new-api/
├── router/web-router.go           # Serves /creative/* routes
├── web/creative/dist/             # Embedded opentu build artifacts
│   ├── index.html
│   └── assets/
└── web/default/src/
    ├── hooks/use-sidebar-data.ts  # Creative Workspace menu config
    └── components/layout/components/
        └── nav-group.tsx          # External link handling
```

### opentu (React Frontend)
```
/mnt/f/code/project/opentu/
├── apps/web/
│   ├── vite.config.ts             # VITE_BASE_URL configuration
│   └── src/
│       ├── main.tsx               # Entry point
│       ├── app/app.tsx            # Main app component (add button here)
│       └── components/            # (Create ReturnButton.tsx here)
└── dist/apps/web/                 # Build output (monorepo root)
    ├── index.html
    └── assets/
```

---

## Design Considerations

### Option 1: Floating Button (Recommended)
```typescript
<button 
  style={{
    position: 'fixed',
    top: '16px',
    left: '16px',
    zIndex: 9999,
    padding: '8px 16px',
    background: 'rgba(0,0,0,0.7)',
    color: 'white',
    border: 'none',
    borderRadius: '6px',
    cursor: 'pointer'
  }}
>
  ← 返回控制台
</button>
```

### Option 2: Integrate with Existing Toolbar
- If opentu has a toolbar/header, add button there
- Requires understanding opentu's layout structure

---

## Risks & Edge Cases

1. **URL Detection Fragility**
   - If opentu adds client-side routing, pathname check may break
   - **Mitigation**: Use explicit environment variable instead

2. **Cross-Origin Issues**
   - If new-api and opentu use different domains in production
   - **Mitigation**: Use relative URLs (`/dashboard`) instead of absolute

3. **State Loss on Navigation**
   - User work in opentu may be lost when clicking return
   - **Mitigation**: Consider adding confirmation dialog if unsaved changes exist

---

## Success Criteria

1. ✅ Button visible only in embedded mode
2. ✅ Clicking button returns to new-api dashboard
3. ✅ No visual regression in opentu UI
4. ✅ Works in both development and production builds
5. ✅ Code is clean, typed, and follows opentu conventions

---

## Dependencies

- **opentu build system**: pnpm, vite, nx
- **new-api build system**: go 1.21+, go:embed
- **Runtime**: Chrome/Firefox latest (opentu's supported browsers)

---

## Notes for Codex

- **Project Root**: `/mnt/f/code/project/opentu` (opentu source)
- **Build Command**: `cd apps/web && pnpm run build`
- **Output Location**: `/mnt/f/code/project/opentu/dist/apps/web/`
- **Deploy Target**: `/mnt/f/code/project/new-api/web/creative/dist/`
- **Current opentu version**: 0.9.6 (from changelog.json)
- **Base path already set**: `/creative/` (verified working)

**Important:** After modifying opentu source, always:
1. Rebuild opentu with `VITE_BASE_URL=/creative/`
2. Copy dist to new-api
3. Rebuild new-api Go binary
4. Restart new-api service

**Verification URL:** http://localhost:3009/creative/

---

## Related Commits

- `1ef09ca`: Phase 0.5 initial integration (theme + menu)
- `1ce404f`: External navigation support for Creative Workspace
- `b6aaa44`: Rebuild opentu with base=/creative/

---

## Questions for Implementation

1. Should the button have a confirmation dialog before navigating?
2. Should it save canvas state before leaving?
3. Preferred icon: `←`, `↩`, or custom SVG?
4. Button text: "返回控制台", "返回", or icon-only?

Default choices (if no input): No confirmation, no state save, `←` icon, full text "返回控制台"
