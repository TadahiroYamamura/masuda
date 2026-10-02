// Spike 2: keep one VM alive for hours and record whether it survives WSL2 idle periods.
import path from "node:path";
import fs from "node:fs";
import { VM } from "@earendil-works/gondolin";
const log = (m) => fs.appendFileSync("keepalive.log", `${new Date().toISOString()} ${m}\n`);
const vm = await VM.create({ sandbox: { imagePath: path.resolve("guest-assets") }, memory: "2G", cpus: 2, sessionLabel: "spike2-keepalive" });
log(`vm up ${vm.id} hostpid=${vm.getHostPid()}`);
fs.writeFileSync("keepalive.pid", String(process.pid));
let n = 0;
setInterval(async () => {
  n++;
  const t0 = Date.now();
  try {
    const r = await vm.exec(["/bin/sh", "-lc", "uptime; date -u +%FT%TZ"]);
    log(`tick ${n} ok=${r.ok} ${Date.now() - t0}ms guest=[${r.stdout.trim().replace(/\n/g, " | ")}]`);
  } catch (e) {
    log(`tick ${n} ERROR ${String(e)}`);
  }
}, 60_000);
process.on("SIGTERM", async () => { log("sigterm, closing"); await vm.close(); process.exit(0); });
