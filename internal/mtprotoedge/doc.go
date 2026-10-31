// Package mtprotoedge is the MTProto connection layer: the TCP/WS listener, key
// exchange, auth key lookup and persistence, message encryption/decryption,
// session/server-salt/msg-id/ack/container/gzip, and unwrapping of wrappers
// such as invokeWithLayer, initConnection and invokeWithoutUpdates.
//
// It only turns the MTProto world into "decrypted RPC requests for an
// identified session" and must not contain business logic.
package mtprotoedge
