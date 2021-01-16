// Copyright 2020 ConsenSys Software Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Code generated by gnark DO NOT EDIT

package eddsa

import (
	"crypto/sha256"
	"testing"

	"github.com/consensys/gnark/crypto/hash"
	"github.com/consensys/gnark/crypto/signature"
	"github.com/consensys/gurvy/bn256/fr"
)

func TestSerialization(t *testing.T) {

	var seed [32]byte
	s := []byte("eddsa")
	for i, v := range s {
		seed[i] = v
	}

	pubKey1, _ := signature.EDDSA_BN256.New(seed)
	pubKey2, _ := signature.EDDSA_BN256.New(seed)

	pubKeyBin1 := pubKey1.Bytes()
	pubKey2.SetBytes(pubKeyBin1)
	pubKeyBin2 := pubKey2.Bytes()
	if len(pubKeyBin1) != len(pubKeyBin2) {
		t.Fatal("Inconistent size")
	}
	for i := 0; i < len(pubKeyBin1); i++ {
		if pubKeyBin1[i] != pubKeyBin2[i] {
			t.Fatal("Error serialize(deserialize(.))")
		}
	}

}

func TestEddsaMIMC(t *testing.T) {

	var seed [32]byte
	s := []byte("eddsa")
	for i, v := range s {
		seed[i] = v
	}

	// create eddsa obj and sign a message
	pubKey, privKey := signature.EDDSA_BN256.New(seed)
	hFunc := hash.MIMC_BN256.New("seed")

	var frMsg fr.Element
	frMsg.SetString("44717650746155748460101257525078853138837311576962212923649547644148297035978")
	msgBin := frMsg.Bytes()
	signature, err := privKey.Sign(msgBin[:], hFunc)
	if err != nil {
		t.Fatal(err)
	}

	// verifies correct msg
	res, err := pubKey.Verify(signature, msgBin[:], hFunc)
	if err != nil {
		t.Fatal(err)
	}
	if !res {
		t.Fatal("Verifiy correct signature should return true")
	}

	// verifies wrong msg
	frMsg.SetString("44717650746155748460101257525078853138837311576962212923649547644148297035979")
	msgBin = frMsg.Bytes()
	res, err = pubKey.Verify(signature, msgBin[:], hFunc)
	if err != nil {
		t.Fatal(err)
	}
	if res {
		t.Fatal("Verfiy wrong signature should be false")
	}

}

func TestEddsaSHA256(t *testing.T) {

	var seed [32]byte
	s := []byte("eddsa")
	for i, v := range s {
		seed[i] = v
	}

	hFunc := sha256.New()

	// create eddsa obj and sign a message
	// create eddsa obj and sign a message

	pubKey, privKey := signature.EDDSA_BN256.New(seed)

	signature, err := privKey.Sign([]byte("message"), hFunc)
	if err != nil {
		t.Fatal(err)
	}

	// verifies correct msg
	res, err := pubKey.Verify(signature, []byte("message"), hFunc)
	if err != nil {
		t.Fatal(err)
	}
	if !res {
		t.Fatal("Verifiy correct signature should return true")
	}

	// verifies wrong msg
	res, err = pubKey.Verify(signature, []byte("wrong_message"), hFunc)
	if err != nil {
		t.Fatal(err)
	}
	if res {
		t.Fatal("Verfiy wrong signature should be false")
	}

}

// benchmarks

func BenchmarkVerify(b *testing.B) {

	var seed [32]byte
	s := []byte("eddsa")
	for i, v := range s {
		seed[i] = v
	}

	hFunc := hash.MIMC_BN256.New("seed")

	// create eddsa obj and sign a message
	pubKey, privKey := signature.EDDSA_BN256.New(seed)
	var frMsg fr.Element
	frMsg.SetString("44717650746155748460101257525078853138837311576962212923649547644148297035978")
	msgBin := frMsg.Bytes()
	signature, _ := privKey.Sign(msgBin[:], hFunc)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pubKey.Verify(signature, msgBin[:], hFunc)
	}
}
