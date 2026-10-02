// ブラウザ（GUI）からループバックの待ち受け（config.jsonのlisten）へつなぐ例。
// バンドラ（Vite等）で@masuda/api-clientと@connectrpc/connect-webを解決して使う。
import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { GateService, WorkspaceService, type WorkspaceEvent } from "@masuda/api-client";

const transport = createConnectTransport({ baseUrl: "http://127.0.0.1:7788" });
export const workspaces = createClient(WorkspaceService, transport);
export const gates = createClient(GateService, transport);

// 再接続しながら全ワークスペースのイベントを受け取り続ける。
export async function follow(onEvent: (ev: WorkspaceEvent) => void, signal: AbortSignal): Promise<void> {
  let after = 0n;
  while (!signal.aborted) {
    try {
      for await (const ev of workspaces.watch({ afterSeq: after }, { signal })) {
        if (ev.seq > after) after = ev.seq;
        onEvent(ev);
      }
      // サーバーが正常にストリームを閉じた = serveが止まった。再起動後は番号が振り直されるので0から。
      after = 0n;
    } catch {
      if (signal.aborted) return;
    }
    await new Promise((r) => setTimeout(r, 1000));
  }
}
