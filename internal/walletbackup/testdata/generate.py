"""Independent Python/OpenSSL known-answer vector for documented TDLib primitives.
All keys and plaintext here are synthetic test data, never production material.
"""
import hashlib,hmac,json,struct
from pathlib import Path
from cryptography.hazmat.primitives.asymmetric import ed25519,x25519
from cryptography.hazmat.primitives.ciphers import Cipher,algorithms,modes
from cryptography.hazmat.primitives.serialization import Encoding,PublicFormat
holder_seed=bytes(range(32));ephemeral_seed=bytes(range(32,64));share=b'wallet-backup-conformance-vector'
pub=ed25519.Ed25519PrivateKey.from_private_bytes(holder_seed).public_key().public_bytes(Encoding.Raw,PublicFormat.Raw)
epub=ed25519.Ed25519PrivateKey.from_private_bytes(ephemeral_seed).public_key().public_bytes(Encoding.Raw,PublicFormat.Raw)
p=2**255-19;y=int.from_bytes(pub,'little')&((1<<255)-1);u=((1+y)*pow(1-y,-1,p)%p).to_bytes(32,'little')
dh=x25519.X25519PrivateKey.from_private_bytes(hashlib.sha512(ephemeral_seed).digest()[:32]).exchange(x25519.X25519PublicKey.from_public_bytes(u))
key=hmac.digest(b'tde2e_shared_secret',dh,'sha512')[:32]
tl=bytes([len(share)])+share;tl+=b'\0'*((-len(tl))%4)
plain=struct.pack('<I',0x8b90dd08)+tl
n=((len(plain)+31)&~15)-len(plain)
prefix=bytes([n])+bytes(range(1,n));payload=prefix+plain
expanded=hmac.digest(key,b'tde2e_encrypt_data','sha512')
msg_id=hmac.digest(expanded[32:],payload+b'\0'*4,'sha256')[:16]
aes=hmac.digest(expanded[:32],msg_id,'sha512')
enc=Cipher(algorithms.AES(aes[:32]),modes.CBC(aes[32:48])).encryptor()
blob=epub+msg_id+enc.update(payload)+enc.finalize()
values=dict(seed=holder_seed,public_key=pub,ephemeral_seed=ephemeral_seed,share=share,shared_secret=key,blob=blob,random=ephemeral_seed+prefix)
Path(__file__).with_name('vector.json').write_text(json.dumps({k:v.hex() for k,v in values.items()},indent=2)+'\n')
