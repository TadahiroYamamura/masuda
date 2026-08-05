# ADR-0030: ワークスペースIDからbranch名プレフィックスを外し、乱数のみにする

## Status

Accepted (2026-08-05)

## Context

[[0014-workspace-id-and-external-state-directory]]は、ワークスペースIDを`<sanitized-branch>-<ランダム6桁hex>`形式（`internal/workspace.NewID`）と定めた。一意性は乱数部分だけで担保されており、branch名部分は「`masuda workspace list`で一覧表示した際にどのbranchに対するワークスペースか一目でわかる可読性」のために付与された、との理由がAlternatives Consideredに明記されている。

このbranch名プレフィックスを見直すきっかけは2つある。

1. ADR-0014が却下したUUID・タイムスタンプ案との比較で挙げた可読性の根拠は、実際には`workspace.Info.Branch`が`Info`構造体の独立したフィールドとして既に永続化されており、`masuda workspace list`・`masuda workspace info`はそこから直接表示している。つまりID文字列自体にbranch名を埋め込まなくても、masuda自身のCLI上の可読性は失われない
2. ADR-0014当時、ワークスペースに人間可読なラベルを付ける機能（`workspace create --name`、`Info.Name`）が存在しなかった。「このワークスペースが何の変更に対応するか」を知る手段がID文字列しかなかったため、branch名をIDに埋め込む以外の選択肢が実質無かった。現在はこのニーズを`Info.Name`という専用のフィールドが担っており、IDにbranch名を重複して持たせる理由が薄れている

残る論点は「masudaのCLIを経由せず、`docker ps`・`tmux ls`・状態ディレクトリの生の一覧を直接見る場面で、branch名のヒントが無くなること」だけだった。しかし、ユーザーにこうした外部ツールを直接触らせる状況は、masudaというツールの設計として本来避けるべき失敗状態であり、そちらのUXを優先して常用する識別子（`masuda plan/review/triage/chat`等、あらゆるコマンドの引数になる）を長く・複雑にするのは本末転倒と判断した。

## Decision

`internal/workspace.NewID`を、branch名を一切使わない乱数のみの識別子生成に変更する（`branch`引数も削除し、`NewID()`に変更）。生成する乱数のバイト数・16進エンコードはADR-0014から変更しない（一意性はここが担保していたため）。

branch名を保持していた`idSanitizer`（branch名の文字種をtmux/dockerで安全な文字集合に正規化するための正規表現）は、branch名をID生成に使わなくなったことで不要になるため削除する。`internal/sandbox.ContainerName`の防御的な`nameSanitizer`（コンテナ名の文字種保証。IDが既に安全な文字集合であることを前提にしつつ、変更に備えて独立に持たせている）はそのまま維持する。

## Alternatives Considered

- **branch名プレフィックスを維持する**: `docker ps`・`tmux ls`を直接見る場面での可読性は保てるが、それは本来ユーザーに晒すべきでない失敗状態のUXであり、そのために常用する識別子を長く・複雑にし続けるのは優先順位が逆だと判断した
- **branch名の代わりに`Info.Name`（ユーザー指定のラベル）をIDに埋め込む**: `Name`は空でもよい任意項目であり、指定されない場合はbranch名にフォールバックすることになって結局ADR-0014と同じ問題（外部ツール向けの可読性のために識別子を汚す）を抱え直すため不採用

## Consequences

- `masuda workspace create <branch>`等が発行するIDは、branch名を含まない短い乱数文字列になる。既存の（ADR-0014形式で作られた）ワークスペースはIDの文字列比較・ディレクトリ存在確認だけで解決されるため、移行作業や後方互換シムは不要
- `docker ps`・`tmux ls`・`~/.local/share/masuda/workspaces/`の生の一覧からは、どのbranchのワークスペースか分からなくなる。確認するには`masuda workspace list`/`info`（`Info.Branch`を表示する）を経由する必要がある
- `CLAUDE.md`・`docs/backlog-agent.md`のID形式の説明は、本ADRへのポインタに置き換える
