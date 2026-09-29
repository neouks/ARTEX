import assert from "node:assert/strict";
import test from "node:test";
import { collapseDigestGraph } from "./exploration-graph.ts";

const nodes = [
 {id:"d",type:"digest",state:"active"},
 {id:"a",type:"intent"},{id:"b",type:"intent"},{id:"f",type:"finding"},
 {id:"g",type:"goal"},{id:"old",type:"digest",state:"superseded"},
];
const edges = [
 {src:"d",dst:"a",rel:"covers"},{src:"d",dst:"b",rel:"covers"},
 {src:"f",dst:"b",rel:"derived_from"}, // deliberately before yields
 {src:"a",dst:"f",rel:"yields"},{src:"b",dst:"f",rel:"yields"},
 {src:"a",dst:"b",rel:"spawns"},{src:"f",dst:"g",rel:"proves"},
 {src:"g",dst:"b",rel:"derived_from"},
];
test("folded inverse edges disappear while other relationships survive",()=>{
 const original=structuredClone({nodes,edges});
 const out=collapseDigestGraph(nodes,edges);
 assert.deepEqual(out.viewNodes.map(n=>n.id),["d","f","g"]);
 assert.deepEqual(out.viewEdges,[
  {src:"d",dst:"f",rel:"yields"}, {src:"f",dst:"g",rel:"proves"},
  {src:"g",dst:"d",rel:"derived_from"},
 ]);
 assert.deepEqual({nodes,edges},original);
});
test("unfolded intents retain distinct finding relations",()=>{
 const out=collapseDigestGraph(nodes.filter(n=>n.type!=="digest"),edges.filter(e=>e.rel!=="covers"));
 assert.ok(out.viewEdges.some(e=>e.src==="f"&&e.dst==="b"&&e.rel==="derived_from")===false); // b also directly yields f
 const distinct=collapseDigestGraph(nodes.filter(n=>n.type!=="digest"),edges.filter(e=>e.rel!=="covers"&&!(e.src==="b"&&e.rel==="yields")));
 assert.ok(distinct.viewEdges.some(e=>e.src==="f"&&e.dst==="b"&&e.rel==="derived_from"));
});
