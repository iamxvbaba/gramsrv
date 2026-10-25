package main

import "testing"

func TestOperatorCommandFingerprintStableAndPasswordSensitive(t *testing.T) {
	params := []byte(`{"username":"carol","permissions":["admins.manage"],"enabled":true}`)
	a := operatorCommandFingerprint("create-admin-operator", params, "secret-one")
	if len(a) != 64 {
		t.Fatalf("fingerprint length=%d, want 64 (sha256 hex)", len(a))
	}
	b := operatorCommandFingerprint("create-admin-operator", params, "secret-one")
	if a != b {
		t.Fatalf("fingerprint not stable: %s vs %s", a, b)
	}
	if a == operatorCommandFingerprint("create-admin-operator", params, "secret-two") {
		t.Fatal("a different password produced the same fingerprint")
	}
	if a == operatorCommandFingerprint("create-admin-operator", []byte(`{"username":"dave"}`), "secret-one") {
		t.Fatal("different params produced the same fingerprint")
	}
	if a == operatorCommandFingerprint("set-admin-operator-password", params, "secret-one") {
		t.Fatal("a different action produced the same fingerprint")
	}
}

func TestSameOperatorRequestSemanticEquality(t *testing.T) {
	a := []byte(`{"actor":"ops","command_id":"x","dry_run":false,"fingerprint":"aa","params":{"id":3},"reason":"r"}`)
	b := []byte(`{ "reason":"r", "params": {"id":3}, "fingerprint": "aa", "dry_run": false, "command_id": "x", "actor": "ops"}`)
	if !sameOperatorRequest(a, b) {
		t.Fatal("semantically equal envelopes were not recognised as the same request")
	}
	changed := []byte(`{"actor":"ops","command_id":"x","dry_run":false,"fingerprint":"bb","params":{"id":3},"reason":"r"}`)
	if sameOperatorRequest(a, changed) {
		t.Fatal("a different fingerprint was treated as the same request")
	}
	bad := []byte(`{"envelope":"broken`)
	if !sameOperatorRequest(bad, bad) {
		t.Fatal("two identical unparseable envelopes should compare by bytes")
	}
}
