// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package std_test

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding"
	"hash"
	"testing"
)

func fuzzHash(f *testing.F, fn func() hash.Hash) {
	f.Add([]byte(""))
	f.Add([]byte("testing"))
	f.Add([]byte("abcdefghij"))
	f.Add([]byte("Free! Free!/A trip/to Mars/for 900/empty jars/Burma Shave"))
	f.Add([]byte("Even if I could be Shakespeare, I think I should still choose to be Faraday. - A. Huxley"))
	f.Fuzz(func(t *testing.T, msg []byte) {
		h := fn()
		initSum := h.Sum(nil)

		n, err := h.Write(msg)
		if err != nil {
			t.Fatal(err)
		}
		if n != len(msg) {
			t.Errorf("got: %d, want: %d", n, len(msg))
		}
		sum := h.Sum(nil)
		if size := h.Size(); len(sum) != size {
			t.Errorf("got: %d, want: %d", len(sum), size)
		}
		if len(msg) > 0 && bytes.Equal(sum, initSum) {
			t.Error("Write didn't change internal hash state")
		}

		var h2 hash.Hash
		// CNG backend does not implement BinaryMarshaler, but it does expose a Clone method.
		if _, ok := h.(encoding.BinaryMarshaler); ok {
			state, err := h.(encoding.BinaryMarshaler).MarshalBinary()
			if err != nil {
				t.Errorf("could not marshal: %v", err)
			}
			h2 = fn()
			if err := h2.(encoding.BinaryUnmarshaler).UnmarshalBinary(state); err != nil {
				t.Errorf("could not unmarshal: %v", err)
			}
		} else {
			h2, err = h.(interface{ Clone() (hash.Hash, error) }).Clone()
			if err != nil {
				t.Fatal(err)
			}
			h.Write(msg)
			h2.Write(msg)
		}
		if actual, actual2 := h.Sum(nil), h2.Sum(nil); !bytes.Equal(actual, actual2) {
			t.Errorf("%q = 0x%x != cloned 0x%x", msg, actual, actual2)
		}
		h.Reset()
		sum = h.Sum(nil)
		if !bytes.Equal(sum, initSum) {
			t.Errorf("got:%x want:%x", sum, initSum)
		}
	})
}

func FuzzSha1(f *testing.F) {
	fuzzHash(f, sha1.New)
}

func FuzzSha224(f *testing.F) {
	fuzzHash(f, sha256.New224)
}

func FuzzSha256(f *testing.F) {
	fuzzHash(f, sha256.New)
}

func FuzzSha384(f *testing.F) {
	fuzzHash(f, sha512.New384)
}

func FuzzSha512(f *testing.F) {
	fuzzHash(f, sha512.New)
}
