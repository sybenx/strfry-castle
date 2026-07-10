# RENOVATION.md — Castle UI de-theming and layout spec

This document is the single source of truth for renaming and restructuring the
Castle sidecar's front page. The project name **Castle** stays. Every other
piece of in-universe medieval vocabulary in user-facing UI is removed and
replaced with plain, self-explanatory language.

Read this whole file before changing anything. When this spec conflicts with
existing code, the spec wins for user-facing strings and page structure; adapt
freely to the codebase's existing conventions for everything else (framework,
components, CSS approach, routing).

---

## 1. Goals

1. A first-time visitor with zero context can answer "are my notes protected
   on this relay, and until when?" without learning any vocabulary.
2. Page order communicates protection level: most protected at the top, least
   protected at the bottom.
3. One page, two audiences: a public view, and an authenticated owner view
   that adds a private layer to the same page (no separate admin UI).
4. No duplicated data: each fact has one canonical section; summary numbers
   link to it.

## 2. Naming migration table

Apply these renames to all user-facing strings: page copy, headings, labels,
tooltips, aria-labels, page titles, error messages, and empty states.

Internal identifiers (variable names, DB columns, event kinds, config keys,
API routes) may keep old names if renaming is risky — but if a rename is cheap
and safe, do it, and note it in the PR description. Never let an internal name
leak into UI copy.

| Old (themed)              | New (plain)                          | Notes |
|---------------------------|--------------------------------------|-------|
| The Lord                  | Owner                                | Header section. "Relay owner" in prose where clarity helps. |
| The Court                 | Members                              | Section heading. The tree itself is the "invite tree" in prose. |
| Favored of the Lord       | Pinned members                       | No standalone section — see §3.3. |
| Ennobled / ennoblement    | Pinned / pinning                     | "Pinned members keep protection even if their inviter is removed." |
| The Citizenry             | (no heading — stat band)             | The summary band needs no title. |
| The Evicted               | Grace period                         | Section heading. Individuals are "removed members," never "evicted." |
| Eviction / evicted        | Removal / removed                    | |
| The Wards                 | Private protections                  | Owner-only section. |
| The Outer Lands           | Unprotected events                   | Section heading. |
| Raid                      | Purge                                | "Purge" is honest and standard for retention tooling. Never "raid." |
| Next Raid                 | Next purge                           | |
| Last Raid                 | Last purge                           | |
| "At the Lord's Pleasure"  | Manual only                          | Value shown when no purge schedule exists. |
| Ethereal lands (DM copy)  | (delete the phrase)                  | See §3.6 for replacement disclaimer text. |
| Castle status: standing   | (cut, or plain health text)          | If a health indicator exists, use "Relay online" / "Relay unreachable." |

**Where the theme is allowed to survive:** the project name "Castle" (title,
readme, repo), the favicon/logo, and — optionally — empty states and the 404
page, where a themed line costs no comprehension (e.g. grace-period empty
state: "No one is in a grace period."; a themed variant is acceptable only if
the plain fact is also present). No themed word may ever be the *only* label
for a feature.

## 3. Page structure (top to bottom)

The vertical order is a protection gradient and must not be reordered:
Owner → summary → Members → [Private protections, owner view only] →
Grace period → Unprotected events → footer.

### 3.1 Owner (header)

- Avatar (kind-0 picture if resolvable, else initials/identicon fallback),
  display name if known, and truncated npub with click-to-copy (copy the full
  npub; confirm with a brief "Copied" state, not an alert).
- One line of plain explanation available (tooltip or subtext): "The relay
  owner controls membership and protection."
- Optional small relay-health chip on the right. Plain language only.

### 3.2 Summary stat band

- 2–4 metric cards in a responsive grid, directly under the header:
  **Members**, **Owner follows**, **Pinned**, **Protected events**.
- Each number is a link/anchor that scrolls to its section (Pinned scrolls to
  Members with the pinned filter active).
- No section heading. Muted small label above, large number below.

### 3.3 Members

- Heading: "Members". Subtext: "People invited to this relay. Their events
  are protected."
- The invite tree renders collapsed to depth 2 by default with expand
  affordances and "+N" counts on collapsed branches. Never render the full
  tree on first paint if it exceeds ~30 nodes.
- Pinned members are marked inline in the tree (a pin icon — not a star, stars
  read as "favorite" in the bookmarking sense; a pin reads as "kept in
  place," which is the actual semantics). Tooltip: "Pinned: stays protected
  even if their inviter is removed."
- Above or beside the tree, a compact row of pinned-member avatars acts as a
  filter: clicking highlights/filters them in the tree. This row **replaces**
  the old standalone "Favored of the Lord" section. Do not build a separate
  pinned list section.
- Members link out to their profile on a standard viewer (njump or
  configurable) via their npub.

### 3.4 Private protections (owner view only)

- Rendered only when authenticated as the owner (see §4). Sits between
  Members and Grace period.
- Heading: "Private protections". Subtext: "Protected like pinned members,
  visible only to you."
- Must leave no trace in the public view: no placeholder, no gap, no
  conditional markup that reveals count or existence.

### 3.5 Grace period

- Rendered **only when non-empty**. When empty, the section is absent
  entirely (or a single muted line if the layout needs it: "No one is in a
  grace period.").
- Heading: "Grace period". Subtext: "Removed members keep event protection
  for 30 days to migrate their notes elsewhere."
- One row per person: name/npub, a progress bar of the window consumed, and a
  countdown ("11d 6h left"). Live countdown is nice-to-have; accurate on page
  load is required.
- This is the only section allowed alarm styling (warm/danger tint). Nothing
  else on the page may use danger colors, so that this section stands out.

### 3.6 Unprotected events

- Heading: "Unprotected events". Visually recessed relative to the sections
  above it: muted text, quieter background. The contrast gradient (protected
  = high contrast, unprotected = low) is a load-bearing design decision —
  keep it.
- Fields: Unprotected event count · Oldest event date · Next purge (schedule,
  or "Manual only") · Last purge (relative time).
- Disclaimer, small italic text at the bottom of this section (not the page
  footer): "Direct messages are not protected and are removed during purges.
  Protecting encrypted DMs is outside the scope of this project."

### 3.7 Footer

- Left: relay websocket URL (click-to-copy) and software/version string
  (e.g. "strfry x.y.z · castle x.y.z"). NIP-11 details may live behind a
  click here rather than earning page space.
- Right: GitHub source link · owner sign-in affordance ("Sign in as owner").

### 3.8 Optional: activity log

If state changes (pins, removals, completed purges, new invites) are already
recorded, add a compact 5-line "Recent activity" list between Members and
Unprotected events. Plain entries: "carol pinned · 2d ago", "purge completed,
31,204 events removed · 18d ago". Skip this if it requires new persistence.

## 4. Public vs owner view

- One page, two layers. Auth via NIP-07 browser extension challenge
  (preferred); never ask for an nsec in a form field.
- Owner view adds: the Private protections section, and inline admin actions
  where they belong — pin/unpin and remove on tree members, "Run purge now"
  in Unprotected events, cancel-removal in Grace period.
- Every destructive action (remove member, run purge) gets a plain
  confirmation stating the concrete consequence: "Remove carol? Her branch
  (4 members) will enter a 30-day grace period." Never a bare "Are you sure?"

## 5. Copy rules

- Sentence case everywhere. No Title Case headings.
- Active voice, verb-first buttons: "Run purge", "Pin member", "Copy npub".
- Every heading gets one line of plain-language subtext the first time a
  concept appears; after that, don't repeat it.
- No exclamation marks, no "please", no "simply/just/easy".
- Errors say what happened and what to do next; never raw exceptions.
- If you catch yourself writing a tooltip to explain a label, the label is
  wrong — fix the label.

## 6. Visual rules (framework-agnostic)

- Hierarchy by contrast and position, not decoration: Owner and Members at
  full contrast; Unprotected events visually recessed; Grace period is the
  sole warm/alarmed element.
- Flat surfaces, hairline borders, generous whitespace. No gradients,
  textures, or ornament — the old parchment/heraldry direction is out.
- Numbers use tabular figures where available; npubs and timestamps in
  monospace.
- Dark mode: if the app has it, every color must be expressed as a token/
  variable that flips; no hardcoded hex in components.
- Collapse-to-summary over scroll: tree collapses, NIP-11 hides behind the
  footer, activity log caps at 5 lines.

## 7. Suggested order of work

1. **Strings pass** — apply §2 renames across all user-facing copy. No layout
   changes yet. Grep for every old term (lord, court, favored, ennoble,
   citizenry, evict, ward, outer, raid, pleasure, ethereal) case-insensitively
   in templates/components and confirm each hit is either renamed or an
   internal identifier deliberately kept.
2. **Structure pass** — reorder/merge sections per §3: stat band to top, fold
   the standalone favored list into the Members tree + avatar filter row,
   make Grace period conditional, move the DM disclaimer into Unprotected
   events, build the footer.
3. **Owner layer** — auth gating, Private protections section, inline admin
   actions with confirmations.
4. **Polish** — contrast gradient, collapse behavior, click-to-copy states,
   countdowns, empty states.

Each pass should be a separate commit/PR so renames are reviewable apart from
structural changes.

## 8. Acceptance checklist

- [ ] No themed vocabulary from §2 remains in any user-facing string
      (case-insensitive grep for the old terms returns only internal
      identifiers or this file).
- [ ] Page order matches §3 exactly.
- [ ] "Pinned" exists only as tree decoration + avatar filter row + stat
      count; no standalone section.
- [ ] Grace period section absent when empty; alarm styling appears nowhere
      else.
- [ ] Public view contains no markup, placeholder, or spacing that reveals
      the existence of Private protections.
- [ ] DM disclaimer lives inside Unprotected events, in plain language.
- [ ] Every stat-band number navigates to its section.
- [ ] All destructive owner actions state their concrete consequence before
      executing.
- [ ] A person who has never heard of this project can read every heading
      and label and know what it means.
