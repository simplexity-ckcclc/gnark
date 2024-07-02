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

package plonk

import (
	"errors"
	"fmt"
	"io"
	"math/big"
	"text/template"
	"time"

	"github.com/consensys/gnark-crypto/ecc"

	curve "github.com/consensys/gnark-crypto/ecc/bn254"

	"github.com/consensys/gnark-crypto/ecc/bn254/fp"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr/hash_to_field"

	"github.com/consensys/gnark-crypto/ecc/bn254/kzg"
	fiatshamir "github.com/consensys/gnark-crypto/fiat-shamir"
	"github.com/consensys/gnark/backend"
	"github.com/consensys/gnark/logger"
)

var (
	errAlgebraicRelation = errors.New("algebraic relation does not hold")
	errInvalidWitness    = errors.New("witness length is invalid")
	errInvalidPoint      = errors.New("point is not on the curve")
)

func Verify(proof *Proof, vk *VerifyingKey, publicWitness fr.Vector, opts ...backend.VerifierOption) error {

	log := logger.Logger().With().Str("curve", "bn254").Str("backend", "plonk").Logger()
	start := time.Now()
	cfg, err := backend.NewVerifierConfig(opts...)
	if err != nil {
		return fmt.Errorf("create backend config: %w", err)
	}

	if len(proof.Bsb22Commitments) != len(vk.Qcp) {
		return errors.New("BSB22 Commitment number mismatch")
	}

	if len(publicWitness) != int(vk.NbPublicVariables) {
		return errInvalidWitness
	}

	// check that the points in the proof are on the curve
	for i := 0; i < len(proof.LRO); i++ {
		if !proof.LRO[i].IsInSubGroup() {
			return errInvalidPoint
		}
	}
	if !proof.Z.IsInSubGroup() {
		return errInvalidPoint
	}
	for i := 0; i < len(proof.H); i++ {
		if !proof.H[i].IsInSubGroup() {
			return errInvalidPoint
		}
	}
	for i := 0; i < len(proof.Bsb22Commitments); i++ {
		if !proof.Bsb22Commitments[i].IsInSubGroup() {
			return errInvalidPoint
		}
	}
	if !proof.BatchedProof.H.IsInSubGroup() {
		return errInvalidPoint
	}
	if !proof.ZShiftedOpening.H.IsInSubGroup() {
		return errInvalidPoint
	}

	// transcript to derive the challenge
	fs := fiatshamir.NewTranscript(cfg.ChallengeHash, "gamma", "beta", "alpha", "zeta")

	// The first challenge is derived using the public data: the commitments to the permutation,
	// the coefficients of the circuit, and the public inputs.
	// derive gamma from the Comm(blinded cl), Comm(blinded cr), Comm(blinded co)
	if err := bindPublicData(fs, "gamma", vk, publicWitness); err != nil {
		return err
	}
	gamma, err := deriveRandomness(fs, "gamma", &proof.LRO[0], &proof.LRO[1], &proof.LRO[2])
	if err != nil {
		return err
	}

	// derive beta from Comm(l), Comm(r), Comm(o)
	beta, err := deriveRandomness(fs, "beta")
	if err != nil {
		return err
	}

	// derive alpha from Com(Z), Bsb22Commitments
	alphaDeps := make([]*curve.G1Affine, len(proof.Bsb22Commitments)+1)
	for i := range proof.Bsb22Commitments {
		alphaDeps[i] = &proof.Bsb22Commitments[i]
	}
	alphaDeps[len(alphaDeps)-1] = &proof.Z
	alpha, err := deriveRandomness(fs, "alpha", alphaDeps...)
	if err != nil {
		return err
	}

	// derive zeta, the point of evaluation
	zeta, err := deriveRandomness(fs, "zeta", &proof.H[0], &proof.H[1], &proof.H[2])
	if err != nil {
		return err
	}

	// evaluation of zhZeta=ζⁿ-1
	var zetaPowerM, zhZeta, lagrangeZero fr.Element
	var bExpo big.Int
	one := fr.One()
	bExpo.SetUint64(vk.Size)
	zetaPowerM.Exp(zeta, &bExpo)
	zhZeta.Sub(&zetaPowerM, &one)  // ζⁿ-1
	lagrangeZero.Sub(&zeta, &one). // ζ-1
					Inverse(&lagrangeZero).         // 1/(ζ-1)
					Mul(&lagrangeZero, &zhZeta).    // (ζ^n-1)/(ζ-1)
					Mul(&lagrangeZero, &vk.SizeInv) // 1/n * (ζ^n-1)/(ζ-1)

	// compute PI = ∑_{i<n} Lᵢ*wᵢ
	var pi fr.Element
	var accw fr.Element
	{
		// [ζ-1,ζ-ω,ζ-ω²,..]
		dens := make([]fr.Element, len(publicWitness))
		accw.SetOne()
		for i := 0; i < len(publicWitness); i++ {
			dens[i].Sub(&zeta, &accw)
			accw.Mul(&accw, &vk.Generator)
		}

		// [1/(ζ-1),1/(ζ-ω),1/(ζ-ω²),..]
		invDens := fr.BatchInvert(dens)

		accw.SetOne()
		var xiLi fr.Element
		for i := 0; i < len(publicWitness); i++ {
			xiLi.Mul(&zhZeta, &invDens[i]).
				Mul(&xiLi, &vk.SizeInv).
				Mul(&xiLi, &accw).
				Mul(&xiLi, &publicWitness[i]) // Pi[i]*(ωⁱ/n)(ζ^n-1)/(ζ-ω^i)
			accw.Mul(&accw, &vk.Generator)
			pi.Add(&pi, &xiLi)
		}

		if cfg.HashToFieldFn == nil {
			cfg.HashToFieldFn = hash_to_field.New([]byte("BSB22-Plonk"))
		}
		var hashedCmt fr.Element
		nbBuf := fr.Bytes
		if cfg.HashToFieldFn.Size() < fr.Bytes {
			nbBuf = cfg.HashToFieldFn.Size()
		}
		var wPowI, den, lagrange fr.Element
		for i, cci := range vk.CommitmentConstraintIndexes {
			cfg.HashToFieldFn.Write(proof.Bsb22Commitments[i].Marshal())
			hashBts := cfg.HashToFieldFn.Sum(nil)
			cfg.HashToFieldFn.Reset()
			hashedCmt.SetBytes(hashBts[:nbBuf])

			// Computing Lᵢ(ζ) where i=CommitmentIndex
			wPowI.Exp(vk.Generator, big.NewInt(int64(vk.NbPublicVariables)+int64(cci)))
			den.Sub(&zeta, &wPowI) // ζ-wⁱ
			lagrange.SetOne().
				Sub(&zetaPowerM, &lagrange). // ζⁿ-1
				Mul(&lagrange, &wPowI).      // wⁱ(ζⁿ-1)
				Div(&lagrange, &den).        // wⁱ(ζⁿ-1)/(ζ-wⁱ)
				Mul(&lagrange, &vk.SizeInv)  // wⁱ/n (ζⁿ-1)/(ζ-wⁱ)

			xiLi.Mul(&lagrange, &hashedCmt)
			pi.Add(&pi, &xiLi)
		}
	}

	var _s1, _s2, tmp fr.Element
	l := proof.BatchedProof.ClaimedValues[1]
	r := proof.BatchedProof.ClaimedValues[2]
	o := proof.BatchedProof.ClaimedValues[3]
	s1 := proof.BatchedProof.ClaimedValues[4]
	s2 := proof.BatchedProof.ClaimedValues[5]

	// Z(ωζ)
	zu := proof.ZShiftedOpening.ClaimedValue

	// α²*L₁(ζ)
	var alphaSquarelagrangeZero fr.Element
	alphaSquarelagrangeZero.Mul(&lagrangeZero, &alpha).
		Mul(&alphaSquarelagrangeZero, &alpha) // α²*L₁(ζ)

	// computing the constant coefficient of the full algebraic relation
	// , corresponding to the value of the linearisation polynomiat at ζ
	// PI(ζ) - α²*L₁(ζ) + α(l(ζ)+β*s1(ζ)+γ)(r(ζ)+β*s2(ζ)+γ)(o(ζ)+γ)*z(ωζ)
	var constLin fr.Element
	constLin.Mul(&beta, &s1).Add(&constLin, &gamma).Add(&constLin, &l)       // (l(ζ)+β*s1(ζ)+γ)
	tmp.Mul(&s2, &beta).Add(&tmp, &gamma).Add(&tmp, &r)                      // (r(ζ)+β*s2(ζ)+γ)
	constLin.Mul(&constLin, &tmp)                                            // (l(ζ)+β*s1(ζ)+γ)(r(ζ)+β*s2(ζ)+γ)
	tmp.Add(&o, &gamma)                                                      // (o(ζ)+γ)
	constLin.Mul(&tmp, &constLin).Mul(&constLin, &alpha).Mul(&constLin, &zu) // α(l(ζ)+β*s1(ζ)+γ)(r(ζ)+β*s2(ζ)+γ)(o(ζ)+γ)*z(ωζ)

	constLin.Sub(&constLin, &alphaSquarelagrangeZero).Add(&constLin, &pi) // PI(ζ) - α²*L₁(ζ) + α(l(ζ)+β*s1(ζ)+γ)(r(ζ)+β*s2(ζ)+γ)(o(ζ)+γ)*z(ωζ)
	constLin.Neg(&constLin)                                               // -[PI(ζ) - α²*L₁(ζ) + α(l(ζ)+β*s1(ζ)+γ)(r(ζ)+β*s2(ζ)+γ)(o(ζ)+γ)*z(ωζ)]

	// check that the opening of the linearised polynomial is equal to -constLin
	openingLinPol := proof.BatchedProof.ClaimedValues[0]
	if !constLin.Equal(&openingLinPol) {
		return errAlgebraicRelation
	}

	// computing the linearised polynomial digest
	// α²*L₁(ζ)*[Z] +
	// _s1*[s3]+_s2*[Z] + l(ζ)*[Ql] +
	// l(ζ)r(ζ)*[Qm] + r(ζ)*[Qr] + o(ζ)*[Qo] + [Qk] + ∑ᵢQcp_(ζ)[Pi_i] -
	// Z_{H}(ζ)*(([H₀] + ζᵐ⁺²*[H₁] + ζ²⁽ᵐ⁺²⁾*[H₂])
	// where
	// _s1 =  α*(l(ζ)+β*s1(ζ)+γ)*(r(ζ)+β*s2(ζ)+γ)*β*Z(μζ)
	// _s2 = -α*(l(ζ)+β*ζ+γ)*(r(ζ)+β*u*ζ+γ)*(o(ζ)+β*u²*ζ+γ)

	// _s1 = α*(l(ζ)+β*s1(β)+γ)*(r(ζ)+β*s2(β)+γ)*β*Z(μζ)
	_s1.Mul(&beta, &s1).Add(&_s1, &l).Add(&_s1, &gamma)                   // (l(ζ)+β*s1(β)+γ)
	tmp.Mul(&beta, &s2).Add(&tmp, &r).Add(&tmp, &gamma)                   // (r(ζ)+β*s2(β)+γ)
	_s1.Mul(&_s1, &tmp).Mul(&_s1, &beta).Mul(&_s1, &alpha).Mul(&_s1, &zu) // α*(l(ζ)+β*s1(β)+γ)*(r(ζ)+β*s2(β)+γ)*β*Z(μζ)

	// _s2 = -α*(l(ζ)+β*ζ+γ)*(r(ζ)+β*u*ζ+γ)*(o(ζ)+β*u²*ζ+γ)
	_s2.Mul(&beta, &zeta).Add(&_s2, &gamma).Add(&_s2, &l)                                                     // (l(ζ)+β*ζ+γ)
	tmp.Mul(&beta, &vk.CosetShift).Mul(&tmp, &zeta).Add(&tmp, &gamma).Add(&tmp, &r)                           // (r(ζ)+β*u*ζ+γ)
	_s2.Mul(&_s2, &tmp)                                                                                       // (l(ζ)+β*ζ+γ)*(r(ζ)+β*u*ζ+γ)
	tmp.Mul(&beta, &vk.CosetShift).Mul(&tmp, &vk.CosetShift).Mul(&tmp, &zeta).Add(&tmp, &o).Add(&tmp, &gamma) // (o(ζ)+β*u²*ζ+γ)
	_s2.Mul(&_s2, &tmp).Mul(&_s2, &alpha).Neg(&_s2)                                                           // -α*(l(ζ)+β*ζ+γ)*(r(ζ)+β*u*ζ+γ)*(o(ζ)+β*u²*ζ+γ)

	// α²*L₁(ζ) - α*(l(ζ)+β*ζ+γ)*(r(ζ)+β*u*ζ+γ)*(o(ζ)+β*u²*ζ+γ)
	var coeffZ fr.Element
	coeffZ.Add(&alphaSquarelagrangeZero, &_s2)

	// l(ζ)*r(ζ)
	var rl fr.Element
	rl.Mul(&l, &r)

	// -ζⁿ⁺²*(ζⁿ-1), -ζ²⁽ⁿ⁺²⁾*(ζⁿ-1), -(ζⁿ-1)
	nPlusTwo := big.NewInt(int64(vk.Size) + 2)
	var zetaNPlusTwoZh, zetaNPlusTwoSquareZh, zh fr.Element
	zetaNPlusTwoZh.Exp(zeta, nPlusTwo)
	zetaNPlusTwoSquareZh.Mul(&zetaNPlusTwoZh, &zetaNPlusTwoZh)                          // ζ²⁽ⁿ⁺²⁾
	zetaNPlusTwoZh.Mul(&zetaNPlusTwoZh, &zhZeta).Neg(&zetaNPlusTwoZh)                   // -ζⁿ⁺²*(ζⁿ-1)
	zetaNPlusTwoSquareZh.Mul(&zetaNPlusTwoSquareZh, &zhZeta).Neg(&zetaNPlusTwoSquareZh) // -ζ²⁽ⁿ⁺²⁾*(ζⁿ-1)
	zh.Neg(&zhZeta)

	var linearizedPolynomialDigest curve.G1Affine
	points := append(proof.Bsb22Commitments,
		vk.Ql, vk.Qr, vk.Qm, vk.Qo, vk.Qk,
		vk.S[2], proof.Z,
		proof.H[0], proof.H[1], proof.H[2],
	)

	qC := make([]fr.Element, len(proof.Bsb22Commitments))
	copy(qC, proof.BatchedProof.ClaimedValues[6:])

	scalars := append(qC,
		l, r, rl, o, one,
		_s1, coeffZ,
		zh, zetaNPlusTwoZh, zetaNPlusTwoSquareZh,
	)
	if _, err := linearizedPolynomialDigest.MultiExp(points, scalars, ecc.MultiExpConfig{}); err != nil {
		return err
	}

	// Fold the first proof
	digestsToFold := make([]curve.G1Affine, len(vk.Qcp)+6)
	copy(digestsToFold[6:], vk.Qcp)
	digestsToFold[0] = linearizedPolynomialDigest
	digestsToFold[1] = proof.LRO[0]
	digestsToFold[2] = proof.LRO[1]
	digestsToFold[3] = proof.LRO[2]
	digestsToFold[4] = vk.S[0]
	digestsToFold[5] = vk.S[1]
	foldedProof, foldedDigest, err := kzg.FoldProof(
		digestsToFold,
		&proof.BatchedProof,
		zeta,
		cfg.KZGFoldingHash,
		zu.Marshal(),
	)
	if err != nil {
		return err
	}

	// Batch verify
	var shiftedZeta fr.Element
	shiftedZeta.Mul(&zeta, &vk.Generator)
	err = kzg.BatchVerifyMultiPoints([]kzg.Digest{
		foldedDigest,
		proof.Z,
	},
		[]kzg.OpeningProof{
			foldedProof,
			proof.ZShiftedOpening,
		},
		[]fr.Element{
			zeta,
			shiftedZeta,
		},
		vk.Kzg,
	)

	log.Debug().Dur("took", time.Since(start)).Msg("verifier done")

	return err
}

func bindPublicData(fs *fiatshamir.Transcript, challenge string, vk *VerifyingKey, publicInputs []fr.Element) error {

	// permutation
	if err := fs.Bind(challenge, vk.S[0].Marshal()); err != nil {
		return err
	}
	if err := fs.Bind(challenge, vk.S[1].Marshal()); err != nil {
		return err
	}
	if err := fs.Bind(challenge, vk.S[2].Marshal()); err != nil {
		return err
	}

	// coefficients
	if err := fs.Bind(challenge, vk.Ql.Marshal()); err != nil {
		return err
	}
	if err := fs.Bind(challenge, vk.Qr.Marshal()); err != nil {
		return err
	}
	if err := fs.Bind(challenge, vk.Qm.Marshal()); err != nil {
		return err
	}
	if err := fs.Bind(challenge, vk.Qo.Marshal()); err != nil {
		return err
	}
	if err := fs.Bind(challenge, vk.Qk.Marshal()); err != nil {
		return err
	}
	for i := range vk.Qcp {
		if err := fs.Bind(challenge, vk.Qcp[i].Marshal()); err != nil {
			return err
		}
	}

	// public inputs
	for i := 0; i < len(publicInputs); i++ {
		if err := fs.Bind(challenge, publicInputs[i].Marshal()); err != nil {
			return err
		}
	}

	return nil

}

func deriveRandomness(fs *fiatshamir.Transcript, challenge string, points ...*curve.G1Affine) (fr.Element, error) {

	var buf [curve.SizeOfG1AffineUncompressed]byte
	var r fr.Element

	for _, p := range points {
		buf = p.RawBytes()
		if err := fs.Bind(challenge, buf[:]); err != nil {
			return r, err
		}
	}

	b, err := fs.ComputeChallenge(challenge)
	if err != nil {
		return r, err
	}
	r.SetBytes(b)
	return r, nil
}

// ExportSolidity exports the verifying key to a solidity smart contract.
//
// See https://github.com/ConsenSys/gnark-tests for example usage.
//
// Code has not been audited and is provided as-is, we make no guarantees or warranties to its safety and reliability.
func (vk *VerifyingKey) ExportSolidity(w io.Writer) error {
	funcMap := template.FuncMap{
		"hex": func(i int) string {
			return fmt.Sprintf("0x%x", i)
		},
		"mul": func(a, b int) int {
			return a * b
		},
		"inc": func(i int) int {
			return i + 1
		},
		"frstr": func(x fr.Element) string {
			// we use big.Int to always get a positive string.
			// not the most efficient hack, but it works better for .sol generation.
			bv := new(big.Int)
			x.BigInt(bv)
			return bv.String()
		},
		"fpstr": func(x fp.Element) string {
			bv := new(big.Int)
			x.BigInt(bv)
			return bv.String()
		},
		"add": func(i, j int) int {
			return i + j
		},
	}

	t, err := template.New("t").Funcs(funcMap).Parse(tmplSolidityVerifier)
	if err != nil {
		return err
	}
	return t.Execute(w, vk)
}
