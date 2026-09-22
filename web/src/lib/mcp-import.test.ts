import assert from "node:assert/strict";
import test from "node:test";
import { normalizeMCPImportConfig } from "./mcp-import";
test("MCP import retains argument arrays and remote transport", () => {
  const result=normalizeMCPImportConfig({mcpServers:{local:{command:"node",args:["a b","c"]},remote:{type:"sse",url:"https://example.test/mcp"}}});
  assert.deepEqual(result.servers[0].args,["a b","c"]);
  assert.equal(result.servers[1].transport,"sse");
  assert.throws(()=>normalizeMCPImportConfig({mcpServers:{bad:{command:""}}}));
});
