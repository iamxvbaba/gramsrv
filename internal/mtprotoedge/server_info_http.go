package mtprotoedge

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"mime"
	"net/http"
	"strconv"

	"telesrv/internal/identity"
)

// ServerInfoPath is the well-known same-port HTTP path a client can GET to
// self-configure an "Add Server" entry from just a host:port -- no more
// manual `openssl rsa -RSAPublicKey_out` + copy-paste of the PEM block.
//
// Served over the same samePortMux HTTP side that already carries WebSocket
// upgrades (see same_port_mux.go): a plain GET here never looks like an
// obfuscated2 init (isHTTPHeaderPrefix's "legal init headers never start
// with GET/POST/HEAD/OPTI" invariant), so this adds no new port and no new
// risk to the raw-TCP detection path. Only reachable when serveMixed is
// active, i.e. TELESRV_WEBSOCKET_ENABLE=true (the default).
//
// Adapted (not copied verbatim) from the sibling project owpengram-server
// (github.com/owpengram/owpengram-server, Apache-2.0), which forks the same
// upstream telesrv base as this project -- renamed off their "/owpengram/"
// path prefix since that's their branding namespace, not a functional
// requirement. No ShuzaGram client currently calls this (see internal/identity's
// package doc comment: ShuzaGram clients build from vanilla Telegram source,
// which has no "Add Server" self-configuring UI to call it from) -- this
// lands the server-side half so a client feature can be added later without
// another round of server work.
const ServerInfoPath = "/server-info"

// ServerIconPath serves the server's icon as a raw image, separately from
// ServerInfoPath's JSON -- avoids inflating every server-info fetch with a
// base64 blob when most callers only need it once, and lets it be cached/
// requested independently (e.g. an <img src> tag).
const ServerIconPath = "/server-icon"

// ServerInfoResponse is ServerInfoPath's JSON body. RSAPublicKeyPEM is the
// PKCS#1 "RSA PUBLIC KEY" PEM block -- the same format
// `openssl rsa -RSAPublicKey_out` produces. Name/Description are
// admin-edited via internal/identity and optional -- clients should treat
// blank as "no override" and keep whatever the user typed.
type ServerInfoResponse struct {
	DCID            int    `json:"dc_id"`
	RSAPublicKeyPEM string `json:"rsa_public_key_pem"`
	Name            string `json:"name,omitempty"`
	Description     string `json:"description,omitempty"`
	// HasIcon tells the client whether GET ServerIconPath is worth calling,
	// without requiring a separate round trip just to find out.
	HasIcon bool `json:"has_icon,omitempty"`
}

// rsaPublicKeyPEM renders key's public half as a PKCS#1 PEM block, matching
// `openssl rsa -in server_rsa.pem -RSAPublicKey_out` byte-for-byte (that
// flag selects PKCS#1 encoding, not the x509/PKIX default `-pubout` would
// produce -- a real format difference, not just a header/footer string).
func rsaPublicKeyPEM(key *rsa.PrivateKey) []byte {
	if key == nil {
		return nil
	}
	der := x509.MarshalPKCS1PublicKey(&key.PublicKey)
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: der})
}

// serverInfoHTTPHandler serves ServerInfoResponse at ServerInfoPath and the
// raw icon bytes at ServerIconPath, delegating every other path to next (the
// existing WebSocket route handler). pubKeyPEM is nil when the server has no
// RSA key configured (shouldn't happen in production -- handshakes would
// already be broken -- but a client asking anyway gets 503, not a panic or
// an empty key). identityStore may be nil (identity feature disabled);
// Name/Description/icon are then simply omitted, RSA key/DC still serve.
func serverInfoHTTPHandler(
	next http.Handler,
	dc int,
	pubKeyPEM []byte,
	identityStore *identity.Store,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case ServerInfoPath:
			serveServerInfo(w, r, dc, pubKeyPEM, identityStore)
		case ServerIconPath:
			serveServerIcon(w, r, identityStore)
		default:
			next.ServeHTTP(w, r)
		}
	})
}

func serveServerInfo(
	w http.ResponseWriter,
	r *http.Request,
	dc int,
	pubKeyPEM []byte,
	identityStore *identity.Store,
) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if len(pubKeyPEM) == 0 {
		http.Error(w, "server key not configured", http.StatusServiceUnavailable)
		return
	}
	resp := ServerInfoResponse{
		DCID:            dc,
		RSAPublicKeyPEM: string(pubKeyPEM),
	}
	if identityStore != nil {
		if info, err := identityStore.Get(); err == nil {
			resp.Name = info.Name
			resp.Description = info.Description
			resp.HasIcon = info.IconExt != ""
		}
	}
	body, err := json.Marshal(resp)
	if err != nil {
		http.Error(w, "encode server info", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	// See serveServerIcon's identical Content-Length comment -- same reason:
	// keeps net/http from chunked-encoding a response a hand-rolled
	// raw-socket client parser can't decode. name/description are short
	// today, but nothing enforces that server-side, so this isn't purely
	// defensive.
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body)
}

func serveServerIcon(w http.ResponseWriter, r *http.Request, identityStore *identity.Store) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if identityStore == nil {
		http.NotFound(w, r)
		return
	}
	data, ext, ok := identityStore.Icon()
	if !ok {
		http.NotFound(w, r)
		return
	}
	contentType := mime.TypeByExtension(ext)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	// Explicit Content-Length keeps net/http from switching to
	// Transfer-Encoding: chunked, which it otherwise does automatically once
	// a single Write() exceeds its small internal sniff buffer (true for
	// basically any real icon, easily hundreds of KB) -- a hand-rolled
	// raw-socket same-port fetch (if a future client feature adds one) would
	// have no chunked-decoding logic to fall back on.
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(data)
}
