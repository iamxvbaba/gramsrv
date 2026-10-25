package mtprotoedge

import "testing"

// CoreExec forwards Conn.clientIP verbatim and Core rejects non-canonical
// literals, so Edge keeps only canonical IPs and drops non-IP peers.
func TestConnSetRemoteAddrKeepsCanonicalIPOnly(t *testing.T) {
	for _, tc := range []struct{ remote, want string }{
		{"127.0.0.1:51234", "127.0.0.1"},
		{"203.0.113.9", "203.0.113.9"},
		{"[2001:DB8::1]:443", "2001:db8::1"},
		{"[::ffff:10.0.0.1]:443", "10.0.0.1"},
		{"[fe80::1%en0]:443", "fe80::1"},
		{"/tmp/telesrv.sock", ""},
		{"@", ""},
		{"", ""},
	} {
		c := &Conn{}
		c.setRemoteAddrStr(tc.remote)
		if got := c.clientIP(); got != tc.want {
			t.Fatalf("setRemoteAddrStr(%q) clientIP = %q, want %q", tc.remote, got, tc.want)
		}
	}
}
