// Package domain holds business entities and value objects (User, Peer, Dialog, Message, MessageID, etc.).
//
// Iron rule: this package must not depend on protocol-layer types such as iamxvbaba/td/tg; TL types are only allowed at the RPC/MTProto boundary.
package domain
