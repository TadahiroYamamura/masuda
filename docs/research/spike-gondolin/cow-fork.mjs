// Can a privileged (root) VM be forked from the workspace VM's disk, and does the
// fork stay isolated from the workspace that continues?
import fs from "node:fs";
import path from "node:path";
import { VM, VmCheckpoint } from "@earendil-works/gondolin";
const img = { sandbox: { imagePath: path.resolve("guest-assets") }, memory: "2G", cpus: 2 };
const ckpt = path.resolve("ws-fork.qcow2");
const t = () => new Date().toISOString().slice(11, 23);
const sh = (vm, c) => vm.exec(["/bin/sh", "-lc", c]).then((r) => (r.stdout + r.stderr).trim());

let t0 = Date.now();
const a = await VM.create({ ...img, sessionLabel: "ws-A" });
console.log(t(), "A up", Date.now() - t0, "ms");
console.log("A rootfs:", await sh(a, "findmnt -no SOURCE,FSTYPE,OPTIONS / ; df -h / | tail -1"));
await sh(a, `set -e; mkdir -p /workspace/repo && cd /workspace/repo && git init -q && git config user.email a@b && git config user.name a
for i in $(seq 1 200); do head -c 20000 /dev/urandom | base64 > f$i.txt; done
git add -A && git commit -qm init && echo 'uncommitted work' > wip.txt && chown -R ubuntu:ubuntu /workspace`);
console.log("A workspace:", await sh(a, "cd /workspace/repo && git log --oneline | head -1 && git status --short && du -sh . | cut -f1"));

t0 = Date.now();
const cp = await a.checkpoint(ckpt);
console.log(t(), "checkpoint took", Date.now() - t0, "ms; size", (fs.statSync(ckpt).size / 1e6).toFixed(1), "MB");
console.log("A usable after checkpoint?", await a.exec(["/bin/true"]).then(() => "yes").catch((e) => "no: " + String(e).slice(0, 80)));

t0 = Date.now();
const loaded = VmCheckpoint.load(ckpt);
const [b, a2] = await Promise.all([loaded.resume({ ...img, sessionLabel: "priv-B" }), loaded.resume({ ...img, sessionLabel: "ws-A2" })]);
console.log(t(), "B and A2 resumed concurrently in", Date.now() - t0, "ms");
console.log("B sees repo:", await sh(b, "cd /workspace/repo && id -un && git log --oneline | head -1 && cat wip.txt && ls | wc -l"));
console.log("B as root modifies:", await sh(b, "cd /workspace/repo && echo evil > f1.txt && echo 'from B' > b-only.txt && rm f2.txt && apt-get -v >/dev/null 2>&1 && echo 'root tools ok'; git status --short | head -4"));
console.log("A2 unaffected? ", await sh(a2, "cd /workspace/repo && git status --short; test -f b-only.txt && echo LEAK || echo 'no leak'; test -f f2.txt && echo f2-present"));
console.log("A2 writes:", await sh(a2, "cd /workspace/repo && echo more > wip2.txt && git status --short | wc -l"));
console.log("B does not see A2:", await sh(b, "test -f /workspace/repo/wip2.txt && echo LEAK || echo 'no leak'"));
// A second checkpoint from A2 (the continuing workspace) to confirm chaining works.
t0 = Date.now();
const cp2 = await a2.checkpoint(path.resolve("ws-fork-2.qcow2"));
console.log(t(), "A2 re-checkpoint took", Date.now() - t0, "ms; size", (fs.statSync(cp2.path).size / 1e6).toFixed(1), "MB");
await b.close();
cp.delete(); cp2.delete();
console.log("cleanup done; files left:", fs.readdirSync(".").filter((f) => f.endsWith(".qcow2")));
