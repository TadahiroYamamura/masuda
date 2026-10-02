// Design assumptions: (1) the egress allowlist can be switched per node while the
// VM runs; (2) the guest reaches a host-local listener through tcp.hosts (MCP path).
import http from "node:http";
import path from "node:path";
import { VM, createHttpHooks } from "@earendil-works/gondolin";

// (2) host-local "MCP" stand-in
const server = http.createServer((req, res) => { res.setHeader("content-type", "text/plain"); res.end(`hello from host ${req.method} ${req.url}\n`); });
await new Promise((r) => server.listen(0, "127.0.0.1", r));
const port = server.address().port;

// (1) mutable policy: node-scoped allowlist
const allowed = new Set(["example.com"]);
const base = createHttpHooks({ allowedHosts: undefined });
const httpHooks = {
  ...base.httpHooks,
  isRequestAllowed: (req) => allowed.has(new URL(req.url).hostname),
};

const vm = await VM.create({
  sandbox: { imagePath: path.resolve("guest-assets") }, memory: "2G", cpus: 2,
  dns: { mode: "synthetic", syntheticHostMapping: "per-host" },
  tcp: { hosts: { "masuda.internal": `127.0.0.1:${port}` } },
  httpHooks, env: base.env, sessionLabel: "spike-policy",
});
try {
  const curl = (u) => vm.exec(["/bin/sh", "-lc", `curl -sS -m 10 -o /dev/null -w '%{http_code}' ${u} 2>&1 || true`]).then((r) => r.stdout.trim().slice(0, 120));
  console.log("example.com (allowed):      ", await curl("https://example.com"));
  console.log("httpbin.org (not allowed):  ", await curl("https://httpbin.org/get"));
  allowed.add("httpbin.org");
  console.log("httpbin.org (after add):    ", await curl("https://httpbin.org/get"));
  allowed.delete("httpbin.org");
  console.log("httpbin.org (after delete): ", await curl("https://httpbin.org/get"));
  const r = await vm.exec(["/bin/sh", "-lc", `curl -sS -m 10 http://masuda.internal:${port}/next_task 2>&1 || true`]);
  console.log("masuda.internal via tcp.hosts:", r.stdout.trim().slice(0, 120));
  const r2 = await vm.exec(["/bin/sh", "-lc", `curl -sS -m 5 -o /dev/null -w '%{http_code}' http://192.168.127.1:${port}/ 2>&1 || true`]);
  console.log("gateway IP directly (should be blocked):", r2.stdout.trim().slice(0, 120));
} finally { await vm.close(); server.close(); }
