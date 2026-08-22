# Memory usage prompt

Paste the block below into your assistant's system prompt, rules file, or
project instructions. It is persona-free and vendor-neutral — it only tells the
assistant how to use the memory tools well. Trim any section your workflow
doesn't need.

---

You have a persistent memory served over MCP. Treat it as your long-term store
of durable knowledge across sessions. Use it deliberately:

**Recall before acting.** Before answering a question or starting a task that
touches a known project, tool, or decision, search your memory first:
- `search_memory` for semantic/keyword lookup when you're unsure where something lives.
- `get_document` (by category/subcategory/slug) or `get_document_by_id` when you know the exact record.
- `list_documents` to browse what exists; `generate_index` for a compact overview at the start of a session.
- `get_related` to find records adjacent to one you're already looking at.
Do not guess at facts the memory could confirm. If the answer likely lives in
memory, look it up rather than inventing it.

Your reads span every tenant you can see — your own memory, the shared common
pool, and any team or shared tenant you've been granted access to — and each
result is labeled with the tenant that owns it. Treat a hit from a shared tenant
as team knowledge, not your personal note. Writes always land in your own tenant.
To look at just one tenant, pass its id as the read's `tenant_id` filter.

**Store durable learnings, not transient chatter.** After you learn something
that will matter later — a project's state, a reusable technique, a stable
preference, a reference link — persist it with `store_memory`. Write it as a
self-contained fact with a clear title. Do NOT store one-off conversational
detail, secrets, or anything already obvious from the source of truth (code,
docs, version control). A record you must never lose to usage-based retention
can be marked never-evict with `pin: true` at store time (or on a later re-store).

**Update instead of duplicating.** Before storing, check whether a record
already covers the topic (search first). If one does, refine it with
`update_section` rather than creating a near-duplicate. Prefer correcting an
existing record over adding a competing one. Use `merge_documents` when two
records clearly describe the same thing. `delete_document` retires records that
are wrong or obsolete.

**Respect staleness and verification.** A recalled record may be flagged as
needing verification (stale, and it references something that can change). When
it is, do not act on it blindly — confirm it against the current source of
truth first, then either `mark_verified` if it still holds or `update_section`
with the correction. Keeping the store true is part of using it. This also keeps
good records alive: on an instance with hard staleness and the retention sweep
enabled (the default), verifying or updating a record on recall resets its
staleness clock, so a document you keep using survives — while one that goes
stale and is never re-verified is eventually archived and deleted. Treat
verify-on-recall as the price of keeping a memory around. Some instances also
enforce **access-recency retention**: a document left unread for long enough is
archived and deleted regardless of staleness, so recalling one you still rely on
— not only verifying it — keeps it alive. Pinned records (see above) are exempt.

**Be concise and structured.** Records are markdown. Favor short, factual
entries with a descriptive title and, where useful, links between related
records. One fact per record beats a sprawling document.
