// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build goexperiment.boringcrypto && linux && cgo && (amd64 || arm64) && !android && !msan

// Package boring provides access to BoringCrypto implementation functions.
// Check the variable Enabled to find out whether BoringCrypto is available.
// If BoringCrypto is not available, the functions in this package all panic.
package backend

import (
	"crypto"
	"crypto/cipher"
	"crypto/internal/boring"
	"hash"
)

const Enabled = true

const RandReader = boring.RandReader

func NewSHA1() hash.Hash   { return boring.NewSHA1() }
func NewSHA224() hash.Hash { return boring.NewSHA224() }
func NewSHA256() hash.Hash { return boring.NewSHA256() }
func NewSHA384() hash.Hash { return boring.NewSHA384() }
func NewSHA512() hash.Hash { return boring.NewSHA512() }

func SHA1(p []byte) (sum [20]byte)   { return boring.SHA1(p) }
func SHA224(p []byte) (sum [28]byte) { return boring.SHA224(p) }
func SHA256(p []byte) (sum [32]byte) { return boring.SHA256(p) }
func SHA384(p []byte) (sum [48]byte) { return boring.SHA384(p) }
func SHA512(p []byte) (sum [64]byte) { return boring.SHA512(p) }

func NewHMAC(h func() hash.Hash, key []byte) hash.Hash { return boring.NewHMAC(h, key) }

func NewAESCipher(key []byte) (cipher.Block, error) { return boring.NewAESCipher(key) }
func NewGCMTLS(c cipher.Block) (cipher.AEAD, error) { return boring.NewGCMTLS(c) }

type PublicKeyECDSA = boring.PublicKeyECDSA
type PrivateKeyECDSA = boring.PrivateKeyECDSA

func GenerateKeyECDSA(curve string) (X, Y, D boring.BigInt, err error) {
	return boring.GenerateKeyECDSA(curve)
}

func NewPrivateKeyECDSA(curve string, X, Y, D boring.BigInt) (*boring.PrivateKeyECDSA, error) {
	return boring.NewPrivateKeyECDSA(curve, X, Y, D)
}

func NewPublicKeyECDSA(curve string, X, Y boring.BigInt) (*boring.PublicKeyECDSA, error) {
	return boring.NewPublicKeyECDSA(curve, X, Y)
}

func SignMarshalECDSA(priv *boring.PrivateKeyECDSA, hash []byte) ([]byte, error) {
	return boring.SignMarshalECDSA(priv, hash)
}

func VerifyECDSA(pub *boring.PublicKeyECDSA, hash []byte, sig []byte) bool {
	return boring.VerifyECDSA(pub, hash, sig)
}

type PublicKeyRSA = boring.PublicKeyRSA
type PrivateKeyRSA = boring.PrivateKeyRSA

func DecryptRSAOAEP(h, mgfHash hash.Hash, priv *boring.PrivateKeyRSA, ciphertext, label []byte) ([]byte, error) {
	return boring.DecryptRSAOAEP(h, mgfHash, priv, ciphertext, label)
}

func DecryptRSAPKCS1(priv *boring.PrivateKeyRSA, ciphertext []byte) ([]byte, error) {
	return boring.DecryptRSAPKCS1(priv, ciphertext)
}

func DecryptRSANoPadding(priv *boring.PrivateKeyRSA, ciphertext []byte) ([]byte, error) {
	return boring.DecryptRSANoPadding(priv, ciphertext)
}

func EncryptRSAOAEP(h, mgfHash hash.Hash, pub *boring.PublicKeyRSA, msg, label []byte) ([]byte, error) {
	return boring.EncryptRSAOAEP(h, mgfHash, pub, msg, label)
}

func EncryptRSAPKCS1(pub *boring.PublicKeyRSA, msg []byte) ([]byte, error) {
	return boring.EncryptRSAPKCS1(pub, msg)
}

func EncryptRSANoPadding(pub *boring.PublicKeyRSA, msg []byte) ([]byte, error) {
	return boring.EncryptRSANoPadding(pub, msg)
}

func GenerateKeyRSA(bits int) (N, E, D, P, Q, Dp, Dq, Qinv boring.BigInt, err error) {
	return boring.GenerateKeyRSA(bits)
}

func NewPrivateKeyRSA(N, E, D, P, Q, Dp, Dq, Qinv boring.BigInt) (*boring.PrivateKeyRSA, error) {
	return boring.NewPrivateKeyRSA(N, E, D, P, Q, Dp, Dq, Qinv)
}

func NewPublicKeyRSA(N, E boring.BigInt) (*boring.PublicKeyRSA, error) {
	return boring.NewPublicKeyRSA(N, E)
}

func SignRSAPKCS1v15(priv *boring.PrivateKeyRSA, h crypto.Hash, hashed []byte) ([]byte, error) {
	return boring.SignRSAPKCS1v15(priv, h, hashed)
}

func SignRSAPSS(priv *boring.PrivateKeyRSA, h crypto.Hash, hashed []byte, saltLen int) ([]byte, error) {
	return boring.SignRSAPSS(priv, h, hashed, saltLen)
}

func VerifyRSAPKCS1v15(pub *boring.PublicKeyRSA, h crypto.Hash, hashed, sig []byte) error {
	return boring.VerifyRSAPKCS1v15(pub, h, hashed, sig)
}

func VerifyRSAPSS(pub *boring.PublicKeyRSA, h crypto.Hash, hashed, sig []byte, saltLen int) error {
	return boring.VerifyRSAPSS(pub, h, hashed, sig, saltLen)
}

type PublicKeyECDH = boring.PublicKeyECDH
type PrivateKeyECDH = boring.PrivateKeyECDH

func ECDH(priv *boring.PrivateKeyECDH, pub *boring.PublicKeyECDH) ([]byte, error) {
	return boring.ECDH(priv, pub)
}

func GenerateKeyECDH(curve string) (*boring.PrivateKeyECDH, []byte, error) {
	return boring.GenerateKeyECDH(curve)
}

func NewPrivateKeyECDH(curve string, bytes []byte) (*boring.PrivateKeyECDH, error) {
	return boring.NewPrivateKeyECDH(curve, bytes)
}

func NewPublicKeyECDH(curve string, bytes []byte) (*boring.PublicKeyECDH, error) {
	return boring.NewPublicKeyECDH(curve, bytes)
}
