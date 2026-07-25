"""
LangGraph orchestrator for the Claude↔LangGraph loop PoC.

Responsibilities:
  - Inspect current project state (which files exist)
  - Decide the next task
  - Overwrite TASK.md with the next instruction (including "DONE" when finished)
  - Exit — Claude picks up from there

All task-domain knowledge lives here, not in CLAUDE.md.
"""

from pathlib import Path
from typing import TypedDict

from langgraph.graph import END, StateGraph


# ---------------------------------------------------------------------------
# State
# ---------------------------------------------------------------------------

class State(TypedDict):
    step: int


# ---------------------------------------------------------------------------
# Task content for each step
# (step 2 is the terminal task: it contains "DONE" so Claude stops)
# ---------------------------------------------------------------------------

TASKS: dict[int, str] = {
    0: """\
# TASK (Step 0): FizzBuzz 関数の実装

## 指示
`fizzbuzz.py` を新規作成し、以下の関数を実装せよ。

```python
def fizzbuzz(n: int) -> list[str]:
    \"\"\"Return FizzBuzz list for 1..n.\"\"\"
```

- 3 と 5 の公倍数 → "FizzBuzz"
- 3 の倍数         → "Fizz"
- 5 の倍数         → "Buzz"
- それ以外         → str(i)

## 完了条件
`fizzbuzz.py` が存在すること
""",
    1: """\
# TASK (Step 1): ユニットテストの作成と実行

## 指示
`test_fizzbuzz.py` を新規作成し、pytest でテストせよ。

カバーすべきケース:
- 通常の数 (1, 2, 4)
- 3 の倍数 (3, 6)
- 5 の倍数 (5, 10)
- 15 の倍数 (15)
- n=15 のリスト全体

## 完了条件
`pytest` で全テストがパスすること
""",
    2: """\
# DONE

すべてのタスクが完了しました。ループを終了してください。

## 実施済み
- `fizzbuzz.py` : FizzBuzz 関数の実装
- `test_fizzbuzz.py`: ユニットテスト（全件パス済み）
""",
}


# ---------------------------------------------------------------------------
# Nodes
# ---------------------------------------------------------------------------

def detect_step(state: State) -> State:
    """Derive the current step from file-system state."""
    if not Path("fizzbuzz.py").exists():
        step = 0
    elif not Path("test_fizzbuzz.py").exists():
        step = 1
    else:
        step = 2
    return {"step": step}


def write_task_md(state: State) -> State:
    """Overwrite TASK.md with the instruction for the current step."""
    content = TASKS[state["step"]]
    Path("TASK.md").write_text(content, encoding="utf-8")
    print(f"[orchestrator] TASK.md written (step={state['step']})")
    return state


# ---------------------------------------------------------------------------
# Graph
# ---------------------------------------------------------------------------

def build_graph():
    g = StateGraph(State)

    g.add_node("detect_step", detect_step)
    g.add_node("write_task_md", write_task_md)

    g.set_entry_point("detect_step")
    g.add_edge("detect_step", "write_task_md")
    g.add_edge("write_task_md", END)

    return g.compile()


# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------

if __name__ == "__main__":
    app = build_graph()
    app.invoke({"step": 0})

    print("\n--- TASK.md ---")
    print(Path("TASK.md").read_text(encoding="utf-8"))
