package runner

import (
	"path/filepath"
	"testing"

	"github.com/TadahiroYamamura/masuda-engine/engine"
)

func TestFileStoreApplyAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "engine.json")
	s, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := s.Apply([]engine.Op{{Kind: engine.OpCheck, Key: "r/a"}, {Kind: engine.OpPut, Key: "r/a", Value: []byte("1")}})
	if err != nil || !ok {
		t.Fatalf("create: %v %v", ok, err)
	}
	// 既にあるキーに「無いこと」を条件にした書き込みは何も書かない。
	ok, err = s.Apply([]engine.Op{{Kind: engine.OpCheck, Key: "r/a"}, {Kind: engine.OpPut, Key: "r/b", Value: []byte("2")}})
	if err != nil || ok {
		t.Fatalf("second create: %v %v", ok, err)
	}
	if _, found, _ := s.Get("r/b"); found {
		t.Fatal("failed Apply must not write")
	}
	s2, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	kvs, err := s2.List("r/")
	if err != nil || len(kvs) != 1 || string(kvs[0].Value) != "1" {
		t.Fatalf("reopened: %v %+v", err, kvs)
	}
}

func TestValidate(t *testing.T) {
	schemas := map[string][]byte{
		"msg": []byte(`{"type":"string","minLength":3}`),
		"obj": []byte(`{"type":"object","required":["a"]}`),
	}
	if p := Validate(schemas, "msg", []byte("feat: x")); p != nil {
		t.Fatalf("string schema takes the raw content: %v", p)
	}
	if p := Validate(schemas, "obj", []byte(`{"b":1}`)); len(p) == 0 {
		t.Fatal("missing required property accepted")
	}
	if p := Validate(schemas, "obj", []byte(`not json`)); len(p) == 0 {
		t.Fatal("non-JSON accepted")
	}
	if p := Validate(schemas, "free", []byte("  \n")); len(p) == 0 {
		t.Fatal("empty data without a schema accepted")
	}
}
