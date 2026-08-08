# Connecting an MCP client

memory-system serves MCP over HTTP at `<base-url>/mcp`, authenticated with a
bearer token (an API key issued via [`memory-admin`](administering.md), or an
OAuth access token). Any MCP-capable client that speaks HTTP transport can
connect. Below: how to generate the config, then two concrete examples.

## Generate the config

Use the built-in emitter — it prints the standard `mcpServers` JSON and never
writes your token to disk:

```sh
memory-admin setup --url https://mem.example.org --token mmcp_your_token_here
# or via env:
MEMORY_URL=https://mem.example.org MEMORY_TOKEN=mmcp_your_token_here memory-admin setup
```

It emits:

```json
{
  "mcpServers": {
    "memory": {
      "type": "http",
      "url": "https://mem.example.org/mcp",
      "headers": {
        "Authorization": "Bearer mmcp_your_token_here"
      }
    }
  }
}
```

The token is a secret — treat the output accordingly and don't commit it. Use
`--name` to change the server key (e.g. `--name work-memory`).

## Examples

These are illustrative for two popular clients; the emitted JSON works with any
MCP client that supports HTTP transport.

### A CLI client that adds MCP servers

```sh
claude mcp add --transport http memory https://mem.example.org/mcp \
  --header "Authorization: Bearer mmcp_your_token_here"
```

### A client that reads an `mcpServers` config block

Paste the emitted JSON (above) into wherever your client reads its MCP server
list. Point additional clients at the same URL with their own tokens.

## Teach the assistant to use it well

Connecting the tools is half the job — also give your assistant guidance on
*how* to use them (recall before acting, store durable learnings, update
instead of duplicating). A ready-to-paste, persona-free block lives in
[`memory-usage-prompt.md`](memory-usage-prompt.md).
