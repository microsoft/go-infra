// Generated code. DO NOT EDIT.

//go:build !(goexperiment.boringcrypto && linux && cgo && (amd64 || arm64) && !android && !msan) && !(goexperiment.cngcrypto && windows) && !(goexperiment.opensslcrypto && linux && cgo)

package backend

import (
	"crypto/cipher"
	"hash"
)

const Enabled = false

func NewSHA1() hash.Hash   { panic("cryptobackend: not available") }
func NewSHA224() hash.Hash { panic("cryptobackend: not available") }
func NewSHA256() hash.Hash { panic("cryptobackend: not available") }
func NewSHA384() hash.Hash { panic("cryptobackend: not available") }
func NewSHA512() hash.Hash { panic("cryptobackend: not available") }

func NewSHA3_256() hash.Hash { panic("cryptobackend: not available") }

func SHA1(p []byte) (sum [20]byte)   { panic("cryptobackend: not available") }
func SHA224(p []byte) (sum [28]byte) { panic("cryptobackend: not available") }
func SHA256(p []byte) (sum [32]byte) { panic("cryptobackend: not available") }
func SHA384(p []byte) (sum [48]byte) { panic("cryptobackend: not available") }
func SHA512(p []byte) (sum [64]byte) { panic("cryptobackend: not available") }

func SHA3_256(p []byte) (sum [64]byte) { panic("cryptobackend: not available") }

func NewHMAC(h func() hash.Hash, key []byte) hash.Hash { panic("cryptobackend: not available") }

func NewAESCipher(key []byte) (cipher.Block, error) { panic("cryptobackend: not available") }
func NewGCMTLS(c cipher.Block) (cipher.AEAD, error) { panic("cryptobackend: not available") }
