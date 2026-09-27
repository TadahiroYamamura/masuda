---
name: trigger-matcher
description: ステップの差分に対して、途中レビューで見るべき観点を選ぶ
tools: Read, Grep, Glob
outputs: [selected-perspectives]
outcomes:
  done: 見るべき観点を選び終えた（0件でもよい）
---
ステップの差分と、各観点のトリガー条件を読み、この差分について途中レビューで見るべき観点だけを選ぶ。当てはまる観点がなければ、空の一覧を書く。
