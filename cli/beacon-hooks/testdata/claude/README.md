# Claude Code payload fixtures

These are `PostToolUse` hook inputs **captured from Claude Code 2.1.291**, saved from the hook's
stdin by a project-level hook that wrote its input to a file. The session ran in beacon-oracle's
sealed container against its scripted mock model and fixture MCP server (scenario
`d19-presence-media-blocks`), so the content is synthetic but the shape is exactly what Claude Code
sends.

Covered payloads:

- `post_tool_use_mcp_media.json` — an MCP tool whose server returned text, an image, audio, a text
  resource, a binary resource and a resource link. Claude Code rewrites the result before the hook
  sees it: the image arrives as an Anthropic image block with its base64 under `source.data`, and
  every other block arrives as text. Audio and the binary resource become notes naming the file
  Claude Code saved them to.
- `post_tool_use_mcp_empty.json` — an MCP tool whose server returned no content blocks, which
  arrives as `"tool_response": []`.

The tokens shaped `BCN-…` are beacon-oracle canaries, not secrets. Replace a fixture with a fresh
capture when a Claude Code release changes the shape.
