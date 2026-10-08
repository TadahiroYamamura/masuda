package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// runPrime はホストのエージェント向けの使い方を出す。initが`.claude/settings.local.json`の
// SessionStartフックに登録し、セッションの開始とcompactionの後に読み込ませる。
// 文面をCLAUDE.local.mdに書き写さずにここから出すのは、masudaの版を上げたときに文面も揃って
// 変わるようにするため。
func runPrime(args []string) error {
	c := newCommand("prime", "prime [--hook-json]")
	hookJSON := c.fs.Bool("hook-json", false, "print as the output of a Claude Code SessionStart hook (additionalContext)")
	if _, err := c.parse(args, 0, 0); err != nil {
		return err
	}
	text, err := templates.ReadFile("templates/prime.md")
	if err != nil {
		return err
	}
	if !*hookJSON {
		_, err = os.Stdout.Write(text)
		return err
	}
	out, err := json.Marshal(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "SessionStart",
			"additionalContext": string(text),
		},
	})
	if err != nil {
		return err
	}
	_, err = fmt.Println(string(out))
	return err
}
