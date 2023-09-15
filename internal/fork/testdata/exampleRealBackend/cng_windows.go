// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build goexperiment.cngcrypto && windows

// Package cng provides access to CNGCrypto implementation functions.
// Check the variable Enabled to find out whether CNGCrypto is available.
// If CNGCrypto is not available, the functions in this package all panic.
package backend

import (
	"crypto"
	"crypto/cipher"
	"crypto/internal/boring/sig"
	"hash"
	_ "unsafe"

	"github.com/microsoft/go-crypto-winnative/cng"
)

// Enabled controls whether FIPS crypto is enabled.
const Enabled = true

func init() {
	// 1: FIPS required: abort the process if the system is not in FIPS mode.
	// other values: continue regardless of system-configured FIPS mode.
	if v, _, ok := envGoFIPS(); ok && v == "1" {
		enabled, err := cng.FIPS()
		if err != nil {
			panic("cngcrypto: unknown FIPS mode: " + err.Error())
		}
		if !enabled {
			panic("cngcrypto: not in FIPS mode")
		}
	}
	sig.BoringCrypto()
}

const RandReader = cng.RandReader

func NewSHA1() hash.Hash {
	return cng.NewSHA1()
}

func NewSHA224() hash.Hash { panic("cngcrypto: not available") }

func NewSHA256() hash.Hash {
	return cng.NewSHA256()
}

func NewSHA384() hash.Hash {
	return cng.NewSHA384()
}

func NewSHA512() hash.Hash {
	return cng.NewSHA512()
}

func NewSHA3_256() hash.Hash {
	return cng.NewSHA3_256()
}

// xcrypto_backend_map:noescape
func SHA1(p []byte) (sum [20]byte) {
	return cng.SHA1(p)
}

// xcrypto_backend_map:noescape
func SHA224(p []byte) (sum [28]byte) { panic("cngcrypto: not available") }

// xcrypto_backend_map:noescape
func SHA256(p []byte) (sum [32]byte) {
	return cng.SHA256(p)
}

// xcrypto_backend_map:noescape
func SHA384(p []byte) (sum [48]byte) {
	return cng.SHA384(p)
}

// xcrypto_backend_map:noescape
func SHA512(p []byte) (sum [64]byte) {
	return cng.SHA512(p)
}

// xcrypto_backend_map:noescape
func SHA3_256(p []byte) (sum [32]byte) {
	return cng.SHA3_256(p)
}

func NewHMAC(h func() hash.Hash, key []byte) hash.Hash {
	return cng.NewHMAC(h, key)
}

func NewAESCipher(key []byte) (cipher.Block, error) {
	return cng.NewAESCipher(key)
}

func NewGCMTLS(c cipher.Block) (cipher.AEAD, error) {
	return cng.NewGCMTLS(c)
}

type PublicKeyECDSA = cng.PublicKeyECDSA
type PrivateKeyECDSA = cng.PrivateKeyECDSA

func GenerateKeyECDSA(curve string) (X, Y, D cng.BigInt, err error) {
	return cng.GenerateKeyECDSA(curve)
}

func NewPrivateKeyECDSA(curve string, X, Y, D cng.BigInt) (*cng.PrivateKeyECDSA, error) {
	return cng.NewPrivateKeyECDSA(curve, X, Y, D)
}

func NewPublicKeyECDSA(curve string, X, Y cng.BigInt) (*cng.PublicKeyECDSA, error) {
	return cng.NewPublicKeyECDSA(curve, X, Y)
}

//go:linkname encodeSignature crypto/ecdsa.encodeSignature
func encodeSignature(r, s []byte) ([]byte, error)

//go:linkname parseSignature crypto/ecdsa.parseSignature
func parseSignature(sig []byte) (r, s []byte, err error)

func SignMarshalECDSA(priv *cng.PrivateKeyECDSA, hash []byte) ([]byte, error) {
	r, s, err := cng.SignECDSA(priv, hash)
	if err != nil {
		return nil, err
	}
	return encodeSignature(r, s)
}

func VerifyECDSA(pub *cng.PublicKeyECDSA, hash []byte, sig []byte) bool {
	rBytes, sBytes, err := parseSignature(sig)
	if err != nil {
		return false
	}
	return cng.VerifyECDSA(pub, hash, cng.BigInt(rBytes), cng.BigInt(sBytes))
}

func SignECDSA(priv *cng.PrivateKeyECDSA, hash []byte) (r, s cng.BigInt, err error) {
	return cng.SignECDSA(priv, hash)
}

func VerifyECDSARaw(pub *cng.PublicKeyECDSA, hash []byte, r, s cng.BigInt) bool {
	return cng.VerifyECDSA(pub, hash, r, s)
}

type PublicKeyRSA = cng.PublicKeyRSA
type PrivateKeyRSA = cng.PrivateKeyRSA

func DecryptRSAOAEP(h, mgfHash hash.Hash, priv *cng.PrivateKeyRSA, ciphertext, label []byte) ([]byte, error) {
	return cng.DecryptRSAOAEP(h, priv, ciphertext, label)
}

func DecryptRSAPKCS1(priv *cng.PrivateKeyRSA, ciphertext []byte) ([]byte, error) {
	return cng.DecryptRSAPKCS1(priv, ciphertext)
}

func DecryptRSANoPadding(priv *cng.PrivateKeyRSA, ciphertext []byte) ([]byte, error) {
	return cng.DecryptRSANoPadding(priv, ciphertext)
}

func EncryptRSAOAEP(h, mgfHash hash.Hash, pub *cng.PublicKeyRSA, msg, label []byte) ([]byte, error) {
	return cng.EncryptRSAOAEP(h, pub, msg, label)
}

func EncryptRSAPKCS1(pub *cng.PublicKeyRSA, msg []byte) ([]byte, error) {
	return cng.EncryptRSAPKCS1(pub, msg)
}

func EncryptRSANoPadding(pub *cng.PublicKeyRSA, msg []byte) ([]byte, error) {
	return cng.EncryptRSANoPadding(pub, msg)
}

func GenerateKeyRSA(bits int) (N, E, D, P, Q, Dp, Dq, Qinv cng.BigInt, err error) {
	return cng.GenerateKeyRSA(bits)
}

func NewPrivateKeyRSA(N, E, D, P, Q, Dp, Dq, Qinv cng.BigInt) (*cng.PrivateKeyRSA, error) {
	return cng.NewPrivateKeyRSA(N, E, D, P, Q, Dp, Dq, Qinv)
}

func NewPublicKeyRSA(N, E cng.BigInt) (*cng.PublicKeyRSA, error) {
	return cng.NewPublicKeyRSA(N, E)
}

func SignRSAPKCS1v15(priv *cng.PrivateKeyRSA, h crypto.Hash, hashed []byte) ([]byte, error) {
	return cng.SignRSAPKCS1v15(priv, h, hashed)
}

func SignRSAPSS(priv *cng.PrivateKeyRSA, h crypto.Hash, hashed []byte, saltLen int) ([]byte, error) {
	return cng.SignRSAPSS(priv, h, hashed, saltLen)
}

func VerifyRSAPKCS1v15(pub *cng.PublicKeyRSA, h crypto.Hash, hashed, sig []byte) error {
	return cng.VerifyRSAPKCS1v15(pub, h, hashed, sig)
}

func VerifyRSAPSS(pub *cng.PublicKeyRSA, h crypto.Hash, hashed, sig []byte, saltLen int) error {
	return cng.VerifyRSAPSS(pub, h, hashed, sig, saltLen)
}

type PrivateKeyECDH = cng.PrivateKeyECDH
type PublicKeyECDH = cng.PublicKeyECDH

func ECDH(priv *cng.PrivateKeyECDH, pub *cng.PublicKeyECDH) ([]byte, error) {
	return cng.ECDH(priv, pub)
}

func GenerateKeyECDH(curve string) (*cng.PrivateKeyECDH, []byte, error) {
	return cng.GenerateKeyECDH(curve)
}

func NewPrivateKeyECDH(curve string, bytes []byte) (*cng.PrivateKeyECDH, error) {
	return cng.NewPrivateKeyECDH(curve, bytes)
}

func NewPublicKeyECDH(curve string, bytes []byte) (*cng.PublicKeyECDH, error) {
	return cng.NewPublicKeyECDH(curve, bytes)
}
