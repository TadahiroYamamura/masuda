---
name: trigger-matcher
description: ステップの差分に対して、途中レビューで見るべき観点を選ぶ
tools: Read, Grep, Glob
inputs: [step-diff]
outputs: [selected-perspectives]
outcomes:
  done: 見るべき観点を選び終えた（0件でもよい）
---
入力のステップの差分（step-diff）と、レビュー観点の一覧（`/workspace/.masuda/reviews/*.md`。ファイル名が観点の名前で、frontmatterの`trigger`がその観点を途中レビューで見るべき変更の条件）を読み、このステップの差分に照らして、レビューする価値がある観点の名前だけを選ぶ。`trigger`を持たない観点は選ばない。判断に迷う場合は含めない方向に倒してよい（見逃しは最終レビューの全観点で拾われるため、途中レビューでの見逃しは早期発見の機会を逃すだけで、正しさ自体は損なわれない）。
