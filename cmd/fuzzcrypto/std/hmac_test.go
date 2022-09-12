// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package std_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"hash"
	"testing"
)

func fuzzHMAC(f *testing.F, fn func() hash.Hash) {
	f.Add([]byte(""))
	f.Add([]byte("testing"))
	f.Add([]byte("abcdefghij"))
	f.Add([]byte("Free! Free!/A trip/to Mars/for 900/empty jars/Burma Shave"))
	f.Add([]byte("Even if I could be Shakespeare, I think I should still choose to be Faraday. - A. Huxley"))
	f.Fuzz(func(t *testing.T, msg []byte) {
		h := hmac.New(fn, nil)
		h.Write([]byte("hello"))
		sumHello := h.Sum(nil)

		h = hmac.New(fn, nil)
		h.Write([]byte("hello world"))
		sumHelloWorld := h.Sum(nil)

		// Test that Sum has no effect on future Sum or Write operations.
		// This is a bit unusual as far as usage, but it's allowed
		// by the definition of Go hash.Hash, and some clients expect it to work.
		h = hmac.New(fn, nil)
		h.Write([]byte("hello"))
		if sum := h.Sum(nil); !bytes.Equal(sum, sumHello) {
			t.Fatalf("1st Sum after hello = %x, want %x", sum, sumHello)
		}
		if sum := h.Sum(nil); !bytes.Equal(sum, sumHello) {
			t.Fatalf("2nd Sum after hello = %x, want %x", sum, sumHello)
		}

		h.Write([]byte(" world"))
		if sum := h.Sum(nil); !bytes.Equal(sum, sumHelloWorld) {
			t.Fatalf("1st Sum after hello world = %x, want %x", sum, sumHelloWorld)
		}
		if sum := h.Sum(nil); !bytes.Equal(sum, sumHelloWorld) {
			t.Fatalf("2nd Sum after hello world = %x, want %x", sum, sumHelloWorld)
		}

		h.Reset()
		h.Write([]byte("hello"))
		if sum := h.Sum(nil); !bytes.Equal(sum, sumHello) {
			t.Fatalf("Sum after Reset + hello = %x, want %x", sum, sumHello)
		}
	})
}

func FuzzHMACSha1(f *testing.F) {
	fuzzHMAC(f, sha1.New)
}

func FuzzHMACSha224(f *testing.F) {
	fuzzHMAC(f, sha256.New224)
}

func FuzzHMACSha256(f *testing.F) {
	fuzzHMAC(f, sha256.New)
}

func FuzzHMACSha384(f *testing.F) {
	fuzzHMAC(f, sha512.New384)
}

func FuzzHMACSha512(f *testing.F) {
	fuzzHMAC(f, sha512.New)
}
