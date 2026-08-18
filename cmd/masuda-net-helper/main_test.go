package main

import (
	"testing"

	"github.com/vishvananda/netlink"
)

// TestIsMasudaTap exercises the safety check createTap/deleteTap rely on
// before ever reusing or removing an existing link by name: it must refuse
// anything that isn't unambiguously a tap this helper itself created,
// because a name collision with an unrelated interface on a shared host
// (another tool's "tap-" prefixed device, or anything else) is a real risk,
// not a theoretical one -- see the tapAliasMarker doc comment.
func TestIsMasudaTap(t *testing.T) {
	tests := []struct {
		name string
		link netlink.Link
		want bool
	}{
		{
			name: "correct type and alias",
			link: &netlink.Tuntap{LinkAttrs: netlink.LinkAttrs{Name: "tap-abc123", Alias: tapAliasMarker}},
			want: true,
		},
		{
			name: "correct type, no alias at all",
			link: &netlink.Tuntap{LinkAttrs: netlink.LinkAttrs{Name: "tap-abc123"}},
			want: false,
		},
		{
			name: "correct type, unrelated alias",
			link: &netlink.Tuntap{LinkAttrs: netlink.LinkAttrs{Name: "tap-abc123", Alias: "someone-elses-tap"}},
			want: false,
		},
		{
			name: "correct alias, wrong type (e.g. a bridge someone named tap-abc123)",
			link: &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "tap-abc123", Alias: tapAliasMarker}},
			want: false,
		},
		{
			name: "correct alias, veth (another common collision-prone type)",
			link: &netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: "tap-abc123", Alias: tapAliasMarker}},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isMasudaTap(tt.link); got != tt.want {
				t.Errorf("isMasudaTap() = %v, want %v", got, tt.want)
			}
		})
	}
}
