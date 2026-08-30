# Connecting an MCP client

memory-system serves MCP over HTTP at `<base-url>/mcp`, authenticated with a
bearer token — an API key (issued through the web console or the `create_api_key`
MCP admin tool; see [administering.md](administering.md)), or an OAuth access
token. Any MCP-capable client that speaks HTTP transport can connect. Below: how
to get the config, then two concrete examples.

## Get the config

Sign in to the web console at `/ui` and open the "Connect an MCP client" panel:
it shows a ready-to-paste `mcpServers` block for your server URL, with a
placeholder where the real key goes. Or write the block by hand:

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

The token is a secret — don't commit it. The `memory` key names the server in
your client; rename it if you run more than one (e.g. `work-memory`).

## Examples

These are illustrative for two popular clients; the block above works with any
MCP client that supports HTTP transport.

### A CLI client that adds MCP servers

```sh
claude mcp add --transport http memory https://mem.example.org/mcp \
  --header "Authorization: Bearer mmcp_your_token_here"
```

### A client that reads an `mcpServers` config block

Paste the JSON block (above) into wherever your client reads its MCP server
list. Point additional clients at the same URL with their own tokens.

## Teach the assistant to use it well

Connecting the tools is half the job — also give your assistant guidance on
*how* to use them (recall before acting, store durable learnings, update
instead of duplicating). A ready-to-paste, persona-free block lives in
[`memory-usage-prompt.md`](memory-usage-prompt.md).
