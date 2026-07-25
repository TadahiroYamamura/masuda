# 作業ループ仕様

## ループルール

1. `TASK.md` が存在しなければ → LangGraph を起動して `TASK.md` を生成させ、2 へ
2. `TASK.md` を読み、指示に従って作業を行う
3. 作業完了後、**終了条件** を確認する
   - 条件を満たしていれば → 以下のコマンドを実行してセッションを終了する（コミット・質問・確認は不要）
     ```bash
     tmux kill-session -t $(tmux display-message -p '#S')
     ```
   - 満たしていなければ → LangGraph を起動して `TASK.md` を上書きさせ、2 へ戻る

## 終了条件

`TASK.md` の本文に `DONE` という文字列が含まれていること

## LangGraph 起動コマンド

```bash
/opt/masuda/venv/bin/python /opt/masuda/orchestrator/implement_graph.py
```
