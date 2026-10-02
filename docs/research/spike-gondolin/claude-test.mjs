// Spike 1: does the Bun-compiled Claude Code work inside Gondolin, through the
// TLS MITM, with the subscription OAuth token injected as a placeholder?
import fs from "node:fs";
import path from "node:path";
import { VM, createHttpHooks, makePlaceholderFunc, BASE62_ALPHABET } from "@earendil-works/gondolin";

const tokenPath = path.join(process.env.HOME, ".local/share/masuda/claude-oauth-token");
const token = fs.readFileSync(tokenPath, "utf8").trim();
const prompt = process.argv[2] ?? "Reply with exactly the word OK and nothing else.";
const extraArgs = process.argv.slice(3);

const requests = [];
const { httpHooks, env } = createHttpHooks({
  allowedHosts: ["api.anthropic.com", "platform.claude.com"],
  secrets: {
    CLAUDE_CODE_OAUTH_TOKEN: {
      hosts: ["api.anthropic.com"],
      value: token,
      placeholder: makePlaceholderFunc({ prefix: "sk-ant-oat01-", length: 80, suffix: "", alphabet: BASE62_ALPHABET }),
    },
  },
  onRequest: (req) => {
    requests.push({ t: Date.now(), m: req.method, u: new URL(req.url).host + new URL(req.url).pathname });
    return undefined;
  },
  onResponse: (res, req) => {
    const e = requests.findLast((r) => r.u === new URL(req.url).host + new URL(req.url).pathname && r.status === undefined);
    if (e) { e.status = res.status; e.ct = res.headers.get("content-type"); e.dt = Date.now() - e.t;
      // Only rate-limit headers: they tell subscription (unified) from pay-as-you-go, and carry no secrets.
      e.rl = [...res.headers.entries()].filter(([k]) => k.startsWith("anthropic-ratelimit-")).map(([k, v]) => `${k}=${v}`).join(" "); }
    return undefined;
  },
});

const vm = await VM.create({
  sandbox: { imagePath: path.resolve("guest-assets") }, memory: "4G", cpus: 4,
  httpHooks,
  env: { ...env, CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: "1", CLAUDE_CODE_MAX_OUTPUT_TOKENS: "64000" },
  sessionLabel: "spike1-claude",
});
console.error("[host] vm up", vm.id);
try {
  // Pass the sandboxd environment through to the ubuntu user explicitly;
  // runuser/sudo would otherwise scrub it.
  const passthru = ["CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS", "CURL_CA_BUNDLE", "CLAUDE_CODE_MAX_OUTPUT_TOKENS"]
    .map((k) => `${k}="$${k}"`).join(" ");
  const q = (s) => `'${s.replace(/'/g, `'\\''`)}'`;
  const cmd = `cd /workspace && exec runuser -u ubuntu -- env HOME=/home/ubuntu USER=ubuntu PATH=/home/ubuntu/.local/bin:/usr/local/bin:/usr/bin:/bin ${passthru} claude -p ${q(prompt)} --output-format json ${extraArgs.map(q).join(" ")}`;
  const t0 = Date.now();
  const r = await vm.exec(["/bin/sh", "-lc", cmd]);
  console.log(`[host] exit=${r.exitCode} elapsed=${((Date.now() - t0) / 1000).toFixed(1)}s`);
  console.log("[stdout]\n" + r.stdout.slice(0, 4000));
  console.log("[stderr]\n" + r.stderr.slice(0, 3000));
  console.log("[requests seen by host]");
  for (const x of requests) console.log(`  ${new Date(x.t).toISOString().slice(11,19)} ${x.m} ${x.u} -> ${x.status ?? "?"} ${x.ct ?? ""} ${x.dt ?? ""}ms ${x.rl ?? ""}`);
} finally {
  await vm.close();
}
