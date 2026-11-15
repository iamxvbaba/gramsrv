package safehttp

import (
	"context"
	"errors"
	"net"
	"reflect"
	"testing"
)

func TestPublicIP(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.100.100.200", "198.18.0.1", "192.0.2.1", "0.1.2.3", "240.0.0.1", "::", "::1", "fc00::1", "fe80::1", "::ffff:127.0.0.1", "64:ff9b::7f00:1", "2002:7f00:1::", "2001:db8::1"} {
		if PublicIP(net.ParseIP(raw)) {
			t.Errorf("accepted non-public IP %s", raw)
		}
	}
	for _, raw := range []string{"8.8.8.8", "1.1.1.1", "::ffff:8.8.8.8", "2606:4700:4700::1111", "2001:4860:4860::8888"} {
		if !PublicIP(net.ParseIP(raw)) {
			t.Errorf("rejected public IP %s", raw)
		}
	}
}

func TestDialPinsResolvedAddressesAndRetriesWithoutDNS(t *testing.T) {
	lookups := 0
	lookup := func(_ context.Context, host string) ([]net.IPAddr, error) {
		lookups++
		if host != "bot.example" || lookups != 1 {
			t.Fatal("unexpected DNS lookup", host, lookups)
		}
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP("2606:4700:4700::1111")}}, nil
	}
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	var destinations []string
	dial := func(_ context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" {
			t.Fatal("unexpected network", network)
		}
		destinations = append(destinations, address)
		if len(destinations) == 1 {
			return nil, errors.New("unreachable")
		}
		return client, nil
	}
	conn, err := dialContext(context.Background(), "tcp", "bot.example:8443", lookup, dial)
	if err != nil || conn != client || !reflect.DeepEqual(destinations, []string{"8.8.8.8:8443", "[2606:4700:4700::1111]:8443"}) {
		t.Fatalf("conn=%v err=%v destinations=%v", conn, err, destinations)
	}
}

func TestDialRejectsUnsafeDNSBeforeConnecting(t *testing.T) {
	for _, ips := range [][]net.IPAddr{
		nil,
		{{IP: net.ParseIP("127.0.0.1")}},
		{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP("10.0.0.1")}},
		{{IP: net.ParseIP("2606:4700:4700::1111"), Zone: "eth0"}},
	} {
		lookup := func(context.Context, string) ([]net.IPAddr, error) { return ips, nil }
		dial := func(context.Context, string, string) (net.Conn, error) {
			t.Fatal("dialed unsafe DNS answer")
			return nil, nil
		}
		if _, err := dialContext(context.Background(), "tcp", "bot.example:443", lookup, dial); err == nil {
			t.Fatal("accepted unsafe DNS answer", ips)
		}
	}
}
