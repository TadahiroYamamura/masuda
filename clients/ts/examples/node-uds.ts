// Node.jsからUDS（masuda serveの既定の待ち受け）へつなぎ、一覧を出してから全ワークスペースのWatchを流す。
//   node examples/node-uds.ts [ソケットのパス]
// ソケットの既定は$XDG_RUNTIME_DIR/masuda.sock。
import { createClient, ConnectError } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";
import { WorkspaceService, WorkspaceState, ActivityKind } from "@masuda/api-client";

const socketPath = process.argv[2] ?? `${process.env.XDG_RUNTIME_DIR}/masuda.sock`;

// UDSなのでホスト名は使われない。baseUrlはURLとして正しければ何でもよい。
const transport = createConnectTransport({
  baseUrl: "http://masuda",
  httpVersion: "1.1",
  nodeOptions: { socketPath },
});
const workspaces = createClient(WorkspaceService, transport);

const { workspaces: list } = await workspaces.list({});
for (const w of list) {
  console.log(w.id, w.branch, WorkspaceState[w.state], ActivityKind[w.activity?.kind ?? 0], w.position);
}

try {
  await workspaces.get({ id: "no-such-workspace" });
} catch (e) {
  console.log("Get:", ConnectError.from(e).code); // 5 = Code.NotFound
}

const ac = new AbortController();
setTimeout(() => ac.abort(), Number(process.env.WATCH_MS ?? 2000));
try {
  for await (const ev of workspaces.watch({ id: "", afterSeq: 0n }, { signal: ac.signal })) {
    console.log(ev.seq, ev.workspaceId || "(serve)", ev.event.case);
  }
} catch (e) {
  if (!ac.signal.aborted) throw e;
}
