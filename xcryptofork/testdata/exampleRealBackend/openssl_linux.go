// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build goexperiment.opensslcrypto && linux && cgo

// Package openssl provides access to OpenSSLCrypto implementation functions.
// Check the variable Enabled to find out whether OpenSSLCrypto is available.
// If OpenSSLCrypto is not available, the functions in this package all panic.
package backend

import (
	"crypto"
	"crypto/cipher"
	"crypto/internal/boring/sig"
	"hash"
	"syscall"
)

// Enabled controls whether FIPS crypto is enabled.
const Enabled = true

// knownVersions is a list of supported and well-known libcrypto.so suffixes in decreasing version order.
// FreeBSD library version numbering does not directly align to the version of OpenSSL.
// Its preferred search order is 11 -> 111.
// Some distributions use 1.0.0 and others (such as Debian) 1.0.2 to refer to the same OpenSSL 1.0.2 version.
// Fedora derived distros use different naming for the version 1.0.x.
var knownVersions = [...]string{"3", "1.1", "11", "111", "1.0.2", "1.0.0", "10"}

func init() {
	version, _ := syscall.Getenv("GO_OPENSSL_VERSION_OVERRIDE")
	if version == "" {
		var fallbackVersion string
		for _, v := range knownVersions {
			exists, fips := openssl.CheckVersion(v)
			if exists && fips {
				version = v
				break
			}
			if exists && fallbackVersion == "" {
				fallbackVersion = v
			}
		}
		if version == "" && fallbackVersion != "" {
			version = fallbackVersion
		}
	}
	if err := openssl.Init(version); err != nil {
		panic("opensslcrypto: can't initialize OpenSSL " + version + ": " + err.Error())
	}
	// 0: FIPS opt-out: abort the process if it is enabled and can't be disabled.
	// 1: FIPS required: abort the process if it is not enabled and can't be enabled.
	// other values: do not override OpenSSL configured FIPS mode.
	var fips string
	if v, _, ok := envGoFIPS(); ok {
		fips = v
	} else if systemFIPSMode() {
		// System configuration can only force FIPS mode.
		fips = "1"
	}
	switch fips {
	case "0":
		if openssl.FIPS() {
			if err := openssl.SetFIPS(false); err != nil {
				panic("opensslcrypto: can't disable FIPS mode for " + openssl.VersionText() + ": " + err.Error())
			}
		}
	case "1":
		if !openssl.FIPS() {
			if err := openssl.SetFIPS(true); err != nil {
				panic("opensslcrypto: can't enable FIPS mode for " + openssl.VersionText() + ": " + err.Error())
			}
		}
	}
	sig.BoringCrypto()
}

func systemFIPSMode() bool {
	var fd int
	for {
		var err error
		fd, err = syscall.Open("/proc/sys/crypto/fips_enabled", syscall.O_RDONLY, 0)
		if err == nil {
			break
		}
		switch err {
		case syscall.EINTR:
			continue
		case syscall.ENOENT:
			return false
		default:
			// If there is an error reading we could either panic or assume FIPS is not enabled.
			// Panicking would be too disruptive for apps that don't require FIPS.
			// If an app wants to be 100% sure that is running in FIPS mode
			// it should use boring.Enabled() or GOFIPS=1.
			return false
		}
	}
	defer syscall.Close(fd)
	var tmp [1]byte
	n, err := syscall.Read(fd, tmp[:])
	if n != 1 || err != nil {
		// We return false instead of panicing for the same reason as before.
		return false
	}
	// fips_enabled can be either '0' or '1'.
	return tmp[0] == '1'
}

const RandReader = openssl.RandReader

func NewSHA1() hash.Hash   { return openssl.NewSHA1() }
func NewSHA224() hash.Hash { return openssl.NewSHA224() }
func NewSHA256() hash.Hash { return openssl.NewSHA256() }
func NewSHA384() hash.Hash { return openssl.NewSHA384() }
func NewSHA512() hash.Hash { return openssl.NewSHA512() }

func NewSHA3_256() hash.Hash { return openssl.NewSHA3_256() }

// xcrypto_backend_map:noescape
func SHA1(p []byte) (sum [20]byte) { return openssl.SHA1(p) }

// xcrypto_backend_map:noescape
func SHA224(p []byte) (sum [28]byte) { return openssl.SHA224(p) }

// xcrypto_backend_map:noescape
func SHA256(p []byte) (sum [32]byte) { return openssl.SHA256(p) }

// xcrypto_backend_map:noescape
func SHA384(p []byte) (sum [48]byte) { return openssl.SHA384(p) }

// xcrypto_backend_map:noescape
func SHA512(p []byte) (sum [64]byte) { return openssl.SHA512(p) }

// xcrypto_backend_map:noescape
func SHA3_256(p []byte) (sum [32]byte) { return openssl.SHA3_256(p) }

func NewHMAC(h func() hash.Hash, key []byte) hash.Hash { return openssl.NewHMAC(h, key) }

func NewAESCipher(key []byte) (cipher.Block, error) { return openssl.NewAESCipher(key) }
func NewGCMTLS(c cipher.Block) (cipher.AEAD, error) { return openssl.NewGCMTLS(c) }

type PublicKeyECDSA = openssl.PublicKeyECDSA
type PrivateKeyECDSA = openssl.PrivateKeyECDSA

func GenerateKeyECDSA(curve string) (X, Y, D openssl.BigInt, err error) {
	return openssl.GenerateKeyECDSA(curve)
}

func NewPrivateKeyECDSA(curve string, X, Y, D openssl.BigInt) (*openssl.PrivateKeyECDSA, error) {
	return openssl.NewPrivateKeyECDSA(curve, X, Y, D)
}

func NewPublicKeyECDSA(curve string, X, Y openssl.BigInt) (*openssl.PublicKeyECDSA, error) {
	return openssl.NewPublicKeyECDSA(curve, X, Y)
}

func SignMarshalECDSA(priv *openssl.PrivateKeyECDSA, hash []byte) ([]byte, error) {
	return openssl.SignMarshalECDSA(priv, hash)
}

func VerifyECDSA(pub *openssl.PublicKeyECDSA, hash []byte, sig []byte) bool {
	return openssl.VerifyECDSA(pub, hash, sig)
}

type PublicKeyRSA = openssl.PublicKeyRSA
type PrivateKeyRSA = openssl.PrivateKeyRSA

func DecryptRSAOAEP(h, mgfHash hash.Hash, priv *openssl.PrivateKeyRSA, ciphertext, label []byte) ([]byte, error) {
	return openssl.DecryptRSAOAEP(h, mgfHash, priv, ciphertext, label)
}

func DecryptRSAPKCS1(priv *openssl.PrivateKeyRSA, ciphertext []byte) ([]byte, error) {
	return openssl.DecryptRSAPKCS1(priv, ciphertext)
}

func DecryptRSANoPadding(priv *openssl.PrivateKeyRSA, ciphertext []byte) ([]byte, error) {
	return openssl.DecryptRSANoPadding(priv, ciphertext)
}

func EncryptRSAOAEP(h, mgfHash hash.Hash, pub *openssl.PublicKeyRSA, msg, label []byte) ([]byte, error) {
	return openssl.EncryptRSAOAEP(h, mgfHash, pub, msg, label)
}

func EncryptRSAPKCS1(pub *openssl.PublicKeyRSA, msg []byte) ([]byte, error) {
	return openssl.EncryptRSAPKCS1(pub, msg)
}

func EncryptRSANoPadding(pub *openssl.PublicKeyRSA, msg []byte) ([]byte, error) {
	return openssl.EncryptRSANoPadding(pub, msg)
}

func GenerateKeyRSA(bits int) (N, E, D, P, Q, Dp, Dq, Qinv openssl.BigInt, err error) {
	return openssl.GenerateKeyRSA(bits)
}

func NewPrivateKeyRSA(N, E, D, P, Q, Dp, Dq, Qinv openssl.BigInt) (*openssl.PrivateKeyRSA, error) {
	return openssl.NewPrivateKeyRSA(N, E, D, P, Q, Dp, Dq, Qinv)
}

func NewPublicKeyRSA(N, E openssl.BigInt) (*openssl.PublicKeyRSA, error) {
	return openssl.NewPublicKeyRSA(N, E)
}

func SignRSAPKCS1v15(priv *openssl.PrivateKeyRSA, h crypto.Hash, hashed []byte) ([]byte, error) {
	return openssl.SignRSAPKCS1v15(priv, h, hashed)
}

func SignRSAPSS(priv *openssl.PrivateKeyRSA, h crypto.Hash, hashed []byte, saltLen int) ([]byte, error) {
	return openssl.SignRSAPSS(priv, h, hashed, saltLen)
}

func VerifyRSAPKCS1v15(pub *openssl.PublicKeyRSA, h crypto.Hash, hashed, sig []byte) error {
	return openssl.VerifyRSAPKCS1v15(pub, h, hashed, sig)
}

func VerifyRSAPSS(pub *openssl.PublicKeyRSA, h crypto.Hash, hashed, sig []byte, saltLen int) error {
	return openssl.VerifyRSAPSS(pub, h, hashed, sig, saltLen)
}

type PublicKeyECDH = openssl.PublicKeyECDH
type PrivateKeyECDH = openssl.PrivateKeyECDH

func ECDH(priv *openssl.PrivateKeyECDH, pub *openssl.PublicKeyECDH) ([]byte, error) {
	return openssl.ECDH(priv, pub)
}

func GenerateKeyECDH(curve string) (*openssl.PrivateKeyECDH, []byte, error) {
	return openssl.GenerateKeyECDH(curve)
}

func NewPrivateKeyECDH(curve string, bytes []byte) (*openssl.PrivateKeyECDH, error) {
	return openssl.NewPrivateKeyECDH(curve, bytes)
}

func NewPublicKeyECDH(curve string, bytes []byte) (*openssl.PublicKeyECDH, error) {
	return openssl.NewPublicKeyECDH(curve, bytes)
}
