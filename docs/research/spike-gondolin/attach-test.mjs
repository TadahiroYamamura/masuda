// Spike 3: tmux session in the guest, reached (a) by exec with pty, (b) by SSH as the ubuntu user.
import path from "node:path";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
const execFileP = promisify(execFile);
import { VM } from "@earendil-works/gondolin";
const vm = await VM.create({ sandbox: { imagePath: path.resolve("guest-assets") }, memory: "2G", cpus: 2, sessionLabel: "spike3-attach" });
try {
  const sh = (c) => vm.exec(["/bin/sh", "-lc", c]);
  let r = await sh("runuser -u ubuntu -- tmux new-session -d -s claude-work 'for i in $(seq 1 1000); do echo tick $i; sleep 1; done'; runuser -u ubuntu -- tmux ls");
  console.log("[tmux ls]", r.stdout.trim(), r.stderr.trim());
  // (a) pty exec: capture a short attach, then detach by sending the tmux prefix+d.
  const proc = vm.exec(["/bin/sh", "-lc", "runuser -u ubuntu -- tmux attach -t claude-work"], { pty: true, stdin: true, stdout: "pipe", stderr: "pipe" });
  let out = "";
  proc.stdout.on("data", (b) => (out += b.toString()));
  await new Promise((res) => setTimeout(res, 3000));
  proc.write("\x02d"); // C-b d
  const pr = await Promise.race([proc, new Promise((res) => setTimeout(() => res({ exitCode: "timeout" }), 5000))]);
  console.log("[pty attach] exit=", pr.exitCode, "saw ticks:", (out.match(/tick \d+/g) || []).slice(-3));
  r = await sh("ls -ld /etc/gondolin /etc/gondolin/mitm /etc/gondolin/mitm/ca.crt; runuser -u ubuntu -- cat /etc/gondolin/mitm/ca.crt >/dev/null && echo ubuntu-can-read-ca || echo ubuntu-cannot-read-ca"); console.log("[ca perms]", r.stdout.trim().replace(/\n/g, " | "), r.stderr.trim());
  // (b) SSH as ubuntu
  const access = await vm.enableSsh({ user: "ubuntu" });
  r = await sh("ps -eo pid,user,args | grep -i [s]sh; ss -ltnp 2>/dev/null | grep 22 || netstat -ltn 2>/dev/null | grep 22; cat /home/ubuntu/.ssh/authorized_keys | cut -c1-40");
  console.log("[guest sshd]", r.stdout.trim().replace(/\n/g, " | "), r.stderr.trim());
  console.log("[ssh] cmd:", access.command.replace(/-i \S+/, "-i <key>"));
  const { stdout: sshOut, stderr: sshErr } = await execFileP("ssh", ["-o", "BatchMode=yes", "-o", "LogLevel=ERROR", "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", "-i", access.identityFile, "-p", String(access.port), `${access.user}@${access.host}`, "id; tmux ls; tmux capture-pane -p -t claude-work | tail -2"], { encoding: "utf8", timeout: 20000 });
  if (sshErr.trim()) console.log("[ssh] stderr:", sshErr.trim());
  console.log("[ssh] out:", sshOut.trim().replace(/\n/g, " | "));
  await access.close();
} finally {
  await vm.close();
}
