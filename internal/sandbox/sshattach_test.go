package sandbox

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var macFormat = regexp.MustCompile(`^52:54:00:[0-9a-f]{2}:[0-9a-f]{2}:[0-9a-f]{2}$`)

func TestMACForIsDeterministicAndWellFormed(t *testing.T) {
	id := "abc123"
	mac1 := MACFor(id)
	mac2 := MACFor(id)
	if mac1 != mac2 {
		t.Errorf("MACFor(%q) not deterministic: %q vs %q", id, mac1, mac2)
	}
	if !macFormat.MatchString(mac1) {
		t.Errorf("MACFor(%q) = %q, want to match %s", id, mac1, macFormat)
	}
	if other := MACFor("def456"); other == mac1 {
		t.Errorf("MACFor() gave the same MAC for two different ids: %q", mac1)
	}
}

func TestLookupGuestIPFindsLease(t *testing.T) {
	dir := t.TempDir()
	leasePath := filepath.Join(dir, "dnsmasq.leases")
	mac := MACFor("workspace1")
	content := "1787000000 " + mac + " 192.168.200.42 guest-workspace1 *\n" +
		"1787000000 aa:bb:cc:dd:ee:ff 192.168.200.7 someone-else *\n"
	if err := os.WriteFile(leasePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	ip, err := LookupGuestIP(mac, leasePath, time.Second)
	if err != nil {
		t.Fatalf("LookupGuestIP() error = %v", err)
	}
	if ip != "192.168.200.42" {
		t.Errorf("LookupGuestIP() = %q, want %q", ip, "192.168.200.42")
	}
}

func TestLookupGuestIPTimesOutWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	leasePath := filepath.Join(dir, "dnsmasq.leases")
	if err := os.WriteFile(leasePath, []byte("1787000000 aa:bb:cc:dd:ee:ff 192.168.200.7 someone-else *\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	_, err := LookupGuestIP(MACFor("no-such-workspace"), leasePath, 300*time.Millisecond)
	if err == nil {
		t.Fatal("LookupGuestIP() = nil error, want a timeout error")
	}
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond {
		t.Errorf("LookupGuestIP() returned after %s, want it to have waited out the timeout", elapsed)
	}
}

func TestSSHAttachArgsShape(t *testing.T) {
	args := SSHAttachArgs("192.168.200.42", "/path/to/key")
	if args[0] != "ssh" {
		t.Fatalf("SSHAttachArgs()[0] = %q, want %q", args[0], "ssh")
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"-i /path/to/key", "ubuntu@192.168.200.42", "tmux attach -t claude-work"} {
		if !strings.Contains(joined, want) {
			t.Errorf("SSHAttachArgs() = %v, want it to contain %q", args, want)
		}
	}
}
