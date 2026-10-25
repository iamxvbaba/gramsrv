// Package serverinfo serves the public "add this server" descriptor that
// OwpenGram-family clients fetch from the MTProto port before their first
// handshake (gramsrv#138):
//
//	GET http://<host>:<mtproto port>/owpengram/server-info
//	GET http://<host>:<mtproto port>/owpengram/server-icon
//
// Everything it exposes is public by definition: the RSA public key and home
// DC the handshake already reveals, plus the operator-edited name,
// description and icon from internal/identity. The endpoint is plain HTTP, so
// clients must treat it as a convenience for filling in the form, not as a
// trust anchor; nothing here grants access to anything.
package serverinfo

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

const (
	// InfoPath returns Info as JSON.
	InfoPath = "/owpengram/server-info"
	// IconPath returns the identity icon bytes, or 404 when none is set.
	IconPath = "/owpengram/server-icon"
)

// Info is the InfoPath body. RSAPublicKeyPEM is a PKCS#1 "RSA PUBLIC KEY"
// block (what `openssl rsa -RSAPublicKey_out` prints and what the clients'
// manual RSA key field expects). Empty Name/Description mean "no override".
type Info struct {
	DCID            int    `json:"dc_id"`
	RSAPublicKeyPEM string `json:"rsa_public_key_pem"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	HasIcon         bool   `json:"has_icon"`
}

// IdentityReader is the read side of identity.Store.
type IdentityReader interface {
	Get() (identity.Info, error)
	Icon() ([]byte, string, bool)
}

// Handler serves InfoPath and IconPath and answers 404 for every other path,
// so it can sit behind the same-port HTTP side of the MTProto listener.
// identities may be nil; the key and DC are still served.
func Handler(dc int, key *rsa.PrivateKey, identities IdentityReader) http.Handler {
	var publicKeyPEM string
	if key != nil {
		publicKeyPEM = string(pem.EncodeToMemory(&pem.Block{
			Type:  "RSA PUBLIC KEY",
			Bytes: x509.MarshalPKCS1PublicKey(&key.PublicKey),
		}))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case InfoPath:
			if !allowRead(w, r) {
				return
			}
			if publicKeyPEM == "" {
				http.Error(w, "server key not configured", http.StatusServiceUnavailable)
				return
			}
			info := Info{DCID: dc, RSAPublicKeyPEM: publicKeyPEM}
			if identities != nil {
				// A broken identity.json must not hide the key: the identity
				// fields are cosmetic and stay empty.
				if stored, err := identities.Get(); err == nil {
					info.Name = stored.Name
					info.Description = stored.Description
					info.HasIcon = stored.IconExt != ""
				}
			}
			body, err := json.Marshal(info)
			if err != nil {
				http.Error(w, "encode server info", http.StatusInternalServerError)
				return
			}
			writeBody(w, r, "application/json; charset=utf-8", body)
		case IconPath:
			if !allowRead(w, r) {
				return
			}
			if identities == nil {
				http.NotFound(w, r)
				return
			}
			data, ext, ok := identities.Icon()
			if !ok {
				http.NotFound(w, r)
				return
			}
			contentType := mime.TypeByExtension(ext)
			if contentType == "" {
				contentType = "application/octet-stream"
			}
			writeBody(w, r, contentType, data)
		default:
			http.NotFound(w, r)
		}
	})
}

func allowRead(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	w.Header().Set("Allow", "GET, HEAD")
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	return false
}

// writeBody sets an explicit Content-Length: the desktop client reads these
// responses with a minimal HTTP/1.1 parser that cannot decode chunked bodies,
// which net/http would otherwise pick for an icon larger than its buffer.
func writeBody(w http.ResponseWriter, r *http.Request, contentType string, body []byte) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body)
}
