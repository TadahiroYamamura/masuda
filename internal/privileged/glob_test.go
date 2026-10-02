package privileged

import "testing"

func TestMatch(t *testing.T) {
	cases := []struct {
		pat, name string
		want      bool
	}{
		{"build/**", "build/artifact.txt", true},
		{"build/**", "build/a/b/c.bin", true},
		{"build/**", "build", false},
		{"build/**", "builds/x", false},
		{"**/*.xml", "report.xml", true},
		{"**/*.xml", "a/b/report.xml", true},
		{"a/**/b.txt", "a/b.txt", true},
		{"a/**/b.txt", "a/x/y/b.txt", true},
		{"out.txt", "out.txt", true},
		{"out.txt", "x/out.txt", false},
		{"*.txt", "a/b.txt", false},
		{"build", "build/a", false},
	}
	for _, c := range cases {
		if got := Match(c.pat, c.name); got != c.want {
			t.Errorf("Match(%q, %q) = %v", c.pat, c.name, got)
		}
	}
}

func TestValidatePattern(t *testing.T) {
	for _, ok := range []string{"build/**", "out.txt", "**/*.xml", "a/[ab]/c"} {
		if err := ValidatePattern(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "/etc/passwd", "../x", "a/../b", "./a", "a//b", "a/[", `a\b`} {
		if err := ValidatePattern(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestBaseDir(t *testing.T) {
	for pat, want := range map[string]string{"build/**": "build", "**/*.xml": ".", "a/b/*.txt": "a/b", "out.txt": "out.txt"} {
		if got := baseDir(pat); got != want {
			t.Errorf("baseDir(%q) = %q, want %q", pat, got, want)
		}
	}
}
