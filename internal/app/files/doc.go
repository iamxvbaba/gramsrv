// Package files is the files application service: upload part accumulation, blob persistence,
// getFile download, and assembling uploaded files into Photo / Document (avatars,
// image/file/sticker messages).
//
// Type boundary: this package uses only domain / store types and does not depend on tg.*;
// the rpc layer handles the tg.InputFileLocation / InputMedia ↔ domain conversion.
package files
