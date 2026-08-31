## ADDED Requirements

### Requirement: Scope-based prompt assembly

The system SHALL provide a retrieval call that returns every prompt document applying to a given
agent and scope, with full section text, in one round trip. This is assembly, not search: no
embedding query, no ranking, no relevance cutoff.

Inputs SHALL be an agent identifier (required) and a scope string (optional — the project name or
working directory the client is starting in). The response SHALL include, per document: path, title,
scope value, delivery lane (always-apply or scoped), content hash, and the ordered section headings
with their full content.

#### Scenario: Assembling an agent's always-apply set

- **WHEN** the call is made for agent `derpy` with no scope
- **THEN** every unscoped prompt document for `derpy` is returned with full section content, and no scoped document is included

#### Scenario: Assembling with a project scope

- **WHEN** the call is made for agent `derpy` with scope `hilo`
- **THEN** the unscoped documents are returned in the always-apply lane and the documents whose scope matches `hilo` are returned in the scoped lane

#### Scenario: Unknown agent

- **WHEN** the call is made for an agent with no prompt documents
- **THEN** an empty result is returned rather than an error

#### Scenario: No semantic ranking applied

- **WHEN** the call is made
- **THEN** every matching document is returned in full, with no similarity score, relevance cutoff, or result limit truncating the set

### Requirement: Deterministic ordering

The retrieval call SHALL return documents in a stable, documented order: ascending by subcategory,
then ascending by slug, with sections in their stored ordinal order. Two calls against unchanged data
SHALL produce byte-identical output.

Instruction precedence beyond that ordering is the client's concern — a client that needs a specific
assembly order imposes it on its own side.

#### Scenario: Repeated calls match

- **WHEN** the same call is issued twice with no intervening writes
- **THEN** both responses are byte-identical

#### Scenario: Documented sort applied

- **WHEN** documents `prompts/derpy/workflow` and `prompts/derpy/persona` both match
- **THEN** `persona` precedes `workflow` in the response

### Requirement: Change detection without refetching content

The response SHALL carry each document's content hash so a client can decide whether its local copy
is current. The system SHALL also support a hash-only mode that returns the same document list with
paths, lanes, and hashes but omits section content.

#### Scenario: Client skips unchanged documents

- **WHEN** a client requests hash-only mode and every returned hash matches what it already has on disk
- **THEN** the client can complete session start with no further calls and no file writes

#### Scenario: Hash changes after an edit

- **WHEN** a prompt document's section content is updated
- **THEN** the document's hash in a subsequent response differs from the previous value

### Requirement: MCP and REST parity

The retrieval call SHALL be exposed both as an MCP tool, for agents holding a memory-system MCP
connection, and as a REST endpoint under `/api/`, for clients that fetch at process start without
speaking MCP. Both SHALL apply identical scoping, ordering, and own-tenant-only rules and SHALL
return the same document set for the same inputs.

#### Scenario: Both surfaces agree

- **WHEN** the MCP tool and the REST endpoint are called with the same agent and scope under the same credentials
- **THEN** both return the same documents, in the same order, with the same hashes

#### Scenario: REST honors tenant scoping

- **WHEN** the REST endpoint is called with an API key belonging to a tenant that holds a viewer grant on another tenant's prompts
- **THEN** only the caller's own tenant's prompt documents are returned
