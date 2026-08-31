## ADDED Requirements

### Requirement: Prompts category and prompt doc_type

The system SHALL accept `prompts` as a document category and SHALL classify documents in that
category as doc_type `prompt`. `prompt` SHALL be a member of `ValidDocTypes` and SHALL have a seeded
`staleness_thresholds` row so threshold lookup never falls through to the reference default.

The canonical path shape SHALL be `prompts/<agent>/<slug>`, where `<agent>` identifies the consuming
agent (e.g. `derpy`) and `<slug>` names one instruction document (e.g. `persona`, `no-slop`).

#### Scenario: Storing a prompt document

- **WHEN** `store_memory` is called with category `prompts`, subcategory `derpy`, slug `persona`, and no explicit doc_type
- **THEN** the document is stored with doc_type `prompt`

#### Scenario: Explicit doc_type is accepted

- **WHEN** a caller passes doc_type `prompt` explicitly
- **THEN** the write succeeds and validation does not reject the value as unknown

#### Scenario: Threshold lookup resolves

- **WHEN** the staleness threshold for doc_type `prompt` is requested
- **THEN** the seeded `prompt` row is returned rather than the `reference` fallback

### Requirement: Prompt documents are exempt from curation machinery

Prompt documents carry operating instructions that are edited in place and never rot on a clock, so
the system SHALL exempt them from every curation mechanism:

- Staleness evaluation SHALL never mark a prompt section `needs_verification`, and hard staleness mode
  SHALL never withhold a prompt section's content.
- `lint_memory` SHALL exclude prompt documents from its stale-document check.
- The nightly cleanup scanner SHALL NOT enqueue near-duplicate pairs involving a prompt document.
- The write-time duplicate guard SHALL NOT fire on a prompt document write.
- Prompt documents SHALL be treated as never-evictable, equivalent to `pinned`, regardless of
  `last_accessed_at`.

This exemption SHALL be a distinct class from the episodic set (`journal`, `handoff`): prompts are
neither append-per-day nor prunable, and widening `episodicDocTypes` would give journals never-evict
semantics they do not have.

#### Scenario: Stale prompt content is still served

- **WHEN** a prompt section was last verified 400 days ago and the tenant's staleness_mode is `hard`
- **THEN** `get_document` returns the section's full content with no `needs_verification` status and no withholding

#### Scenario: Near-identical prompts both survive

- **WHEN** two prompt documents for different agents have section cosine similarity above the scan threshold
- **THEN** the cleanup scanner enqueues no pair for them

#### Scenario: Duplicate guard does not block a prompt write

- **WHEN** `store_memory` writes a prompt document whose centroid is above the tenant duplicate threshold against an existing prompt document
- **THEN** the write succeeds with no `similar_exists` status and no `force` flag required

#### Scenario: Lint ignores prompt staleness

- **WHEN** `lint_memory` runs in a tenant holding prompt documents older than any threshold
- **THEN** no prompt document appears in the stale-document findings

### Requirement: Prompt documents resolve own-tenant only

A prompt document is instructions the consuming agent will execute, so the system SHALL restrict
prompt reads to the caller's home tenant. Prompt documents SHALL NOT be resolved from the common pool
or from any tenant readable through a grant, on any read path — retrieval, `get_document`,
`list_documents`, `search_memory`, or `get_related`.

Writes are unaffected: they already land in the caller's own tenant.

#### Scenario: Common-pool prompt is not returned

- **WHEN** a prompt document exists in the common pool and a tenant fetches prompts
- **THEN** the common-pool document is absent from the result

#### Scenario: Granted tenant's prompt is not returned

- **WHEN** the caller holds a viewer grant on another tenant that has prompt documents
- **THEN** those documents are absent from every prompt read path, while that tenant's non-prompt documents remain readable as before

### Requirement: Prompt documents are excluded from semantic search by default

`search_memory` SHALL omit prompt documents from results unless the caller explicitly narrows to them
via the existing `category` or `doc_type` filters. Instruction text overlaps knowledge text heavily
and would otherwise displace real answers.

#### Scenario: Unfiltered search omits prompts

- **WHEN** `search_memory` is called with a query that lexically matches a prompt document and no category or doc_type filter
- **THEN** no prompt document appears in the results

#### Scenario: Explicit filter includes prompts

- **WHEN** `search_memory` is called with `category="prompts"` or `doc_type="prompt"`
- **THEN** matching prompt documents are returned normally

### Requirement: Prompt scope targeting

A prompt document SHALL carry an optional scope value that decides when it applies:

- An unset or empty scope means the document applies to every session for its agent. These are the
  always-apply instructions a client renders to disk.
- A non-empty scope is a space-separated list of match patterns (project name or path glob). The
  document applies only when the requesting client's scope matches at least one pattern. These are
  the situational instructions a client injects per session.

The scope SHALL be settable and updatable through the write path and SHALL be returned on every read
of the document.

#### Scenario: Unscoped prompt always applies

- **WHEN** a prompt document has no scope value and prompts are fetched for agent `derpy` with any project scope
- **THEN** the document is included and marked as always-apply

#### Scenario: Scoped prompt matches

- **WHEN** a prompt document has scope `hilo memory-system` and prompts are fetched with project scope `hilo`
- **THEN** the document is included and marked as scoped

#### Scenario: Scoped prompt does not match

- **WHEN** the same document is fetched with project scope `a1`
- **THEN** the document is absent from the result
