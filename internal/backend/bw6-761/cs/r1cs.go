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

package cs

import (
	"errors"
	"fmt"
	"io"
	"math/big"
	"strings"

	"github.com/fxamacker/cbor/v2"

	"github.com/consensys/gnark/backend"
	"github.com/consensys/gnark/internal/backend/compiled"
	"github.com/consensys/gnark/internal/backend/ioutils"

	"github.com/consensys/gnark-crypto/ecc"
	"text/template"

	"github.com/consensys/gnark-crypto/ecc/bw6-761/fr"
)

// R1CS decsribes a set of R1CS constraint
type R1CS struct {
	compiled.R1CS
	Coefficients []fr.Element // R1C coefficients indexes point here
}

// NewR1CS returns a new R1CS and sets cs.Coefficient (fr.Element) from provided big.Int values
func NewR1CS(cs compiled.R1CS, coefficients []big.Int) *R1CS {
	r := R1CS{
		R1CS:         cs,
		Coefficients: make([]fr.Element, len(coefficients)),
	}
	for i := 0; i < len(coefficients); i++ {
		r.Coefficients[i].SetBigInt(&coefficients[i])
	}

	return &r
}

// Solve sets all the wires and returns the a, b, c vectors.
// the cs system should have been compiled before. The entries in a, b, c are in Montgomery form.
// a, b, c vectors: ab-c = hz
// witness = [publicWires | secretWires] (without the ONE_WIRE !)
// returns  [publicWires | secretWires | internalWires ]
func (cs *R1CS) Solve(witness, a, b, c []fr.Element, opt backend.ProverOption) ([]fr.Element, error) {

	nbWires := cs.NbPublicVariables + cs.NbSecretVariables + cs.NbInternalVariables
	solution, err := newSolution(nbWires, opt.HintFunctions, cs.Coefficients)
	if err != nil {
		return make([]fr.Element, nbWires), err
	}

	if len(witness) != int(cs.NbPublicVariables-1+cs.NbSecretVariables) { // - 1 for ONE_WIRE
		return solution.values, fmt.Errorf("invalid witness size, got %d, expected %d = %d (public - ONE_WIRE) + %d (secret)", len(witness), int(cs.NbPublicVariables-1+cs.NbSecretVariables), cs.NbPublicVariables-1, cs.NbSecretVariables)
	}

	// compute the wires and the a, b, c polynomials
	if len(a) != len(cs.Constraints) || len(b) != len(cs.Constraints) || len(c) != len(cs.Constraints) {
		return solution.values, errors.New("invalid input size: len(a, b, c) == len(Constraints)")
	}

	solution.solved[0] = true // ONE_WIRE
	solution.values[0].SetOne()
	copy(solution.values[1:], witness) // TODO factorize
	for i := 0; i < len(witness); i++ {
		solution.solved[i+1] = true
	}

	// keep track of the number of wire instantiations we do, for a sanity check to ensure
	// we instantiated all wires
	solution.nbSolved += len(witness) + 1

	// now that we know all inputs are set, defer log printing once all solution.values are computed
	// (or sooner, if a constraint is not satisfied)
	defer solution.printLogs(opt.LoggerOut, cs.Logs)

	// check if there is an inconsistant constraint
	var check fr.Element

	// for each constraint
	// we are guaranteed that each R1C contains at most one unsolved wire
	// first we solve the unsolved wire (if any)
	// then we check that the constraint is valid
	// if a[i] * b[i] != c[i]; it means the constraint is not satisfied
	for i := 0; i < len(cs.Constraints); i++ {
		// solve the constraint, this will compute the missing wire of the gate
		// fmt.Printf("%d\n", i)
		if err := cs.solveConstraint(cs.Constraints[i], &solution); err != nil {
			if dID, ok := cs.MDebug[i]; ok {
				debugInfoStr := solution.logValue(cs.DebugInfo[dID])
				return solution.values, fmt.Errorf("%w: %s", err, debugInfoStr)
			}
			return solution.values, err
		}

		// compute values for the R1C (ie value * coeff)
		a[i], b[i], c[i] = cs.instantiateR1C(cs.Constraints[i], &solution)

		// ensure a[i] * b[i] == c[i]
		check.Mul(&a[i], &b[i])
		if !check.Equal(&c[i]) {
			if dID, ok := cs.MDebug[i]; ok {
				debugInfoStr := solution.logValue(cs.DebugInfo[dID])
				return solution.values, fmt.Errorf("%w: %s", ErrUnsatisfiedConstraint, debugInfoStr)
			}
			return solution.values, ErrUnsatisfiedConstraint
		}
	}

	// sanity check; ensure all wires are marked as "instantiated"
	if !solution.isValid() {
		panic("solver didn't instantiate all wires")
	}

	return solution.values, nil
}

// IsSolved returns nil if given witness solves the R1CS and error otherwise
// this method wraps cs.Solve() and allocates cs.Solve() inputs
func (cs *R1CS) IsSolved(witness []fr.Element, opt backend.ProverOption) error {
	a := make([]fr.Element, len(cs.Constraints))
	b := make([]fr.Element, len(cs.Constraints))
	c := make([]fr.Element, len(cs.Constraints))
	_, err := cs.Solve(witness, a, b, c, opt)
	return err
}

// mulByCoeff sets res = res * t.Coeff
func (cs *R1CS) mulByCoeff(res *fr.Element, t compiled.Term) {
	cID := t.CoeffID()
	switch cID {
	case compiled.CoeffIdOne:
		return
	case compiled.CoeffIdMinusOne:
		res.Neg(res)
	case compiled.CoeffIdZero:
		res.SetZero()
	case compiled.CoeffIdTwo:
		res.Double(res)
	default:
		res.Mul(res, &cs.Coefficients[cID])
	}
}

// compute left, right, o part of a cs constraint
// this function is called when all the wires have been computed
// it instantiates the l, r o part of a R1C
func (cs *R1CS) instantiateR1C(r compiled.R1C, solution *solution) (a, b, c fr.Element) {
	var v fr.Element
	for _, t := range r.L.LinExp {
		v = solution.computeTerm(t)
		a.Add(&a, &v)
	}
	for _, t := range r.R.LinExp {
		v = solution.computeTerm(t)
		b.Add(&b, &v)
	}
	for _, t := range r.O.LinExp {
		v = solution.computeTerm(t)
		c.Add(&c, &v)
	}
	return
}

// solveR1c computes a wire by solving a cs
// the function searches for the unset wire (either the unset wire is
// alone, or it can be computed without ambiguity using the other computed wires
// , eg when doing a binary decomposition: either way the missing wire can
// be computed without ambiguity because the cs is correctly ordered)
//
// It returns the 1 if the the position to solve is in the quadratic part (it
// means that there is a division and serves to navigate in the log info for the
// computational constraints), and 0 otherwise.
func (cs *R1CS) solveConstraint(r compiled.R1C, solution *solution) error {

	// the index of the non zero entry shows if L, R or O has an uninstantiated wire
	// the content is the ID of the wire non instantiated
	var loc uint8

	var a, b, c fr.Element
	var termToCompute compiled.Term

	processTerm := func(t compiled.Term, val *fr.Element, locValue uint8) error {
		vID := t.VariableID()

		// wire is already computed, we just accumulate in val
		if solution.solved[vID] {
			v := solution.computeTerm(t)
			val.Add(val, &v)
			return nil
		}

		// first we check if this is a hint wire
		if hint, ok := cs.MHints[vID]; ok {
			if err := solution.solveWithHint(vID, hint); err != nil {
				return err
			}
			v := solution.computeTerm(t)
			val.Add(val, &v)
			return nil
		}

		if loc != 0 {
			panic("found more than one wire to instantiate")
		}
		termToCompute = t
		loc = locValue
		return nil
	}

	for _, t := range r.L.LinExp {
		if err := processTerm(t, &a, 1); err != nil {
			return err
		}
	}

	for _, t := range r.R.LinExp {
		if err := processTerm(t, &b, 2); err != nil {
			return err
		}
	}

	for _, t := range r.O.LinExp {
		if err := processTerm(t, &c, 3); err != nil {
			return err
		}
	}

	if loc == 0 {
		// there is nothing to solve, may happen if we have an assertion
		// (ie a constraints that doesn't yield any output)
		// or if we solved the unsolved wires with hint functions
		return nil
	}

	// we compute the wire value and instantiate it
	vID := termToCompute.VariableID()

	// solver result
	var wire fr.Element

	switch loc {
	case 1:
		if !b.IsZero() {
			wire.Div(&c, &b).
				Sub(&wire, &a)
			cs.mulByCoeff(&wire, termToCompute)
		}
	case 2:
		if !a.IsZero() {
			wire.Div(&c, &a).
				Sub(&wire, &b)
			cs.mulByCoeff(&wire, termToCompute)
		}
	case 3:
		wire.Mul(&a, &b).
			Sub(&wire, &c)
		cs.mulByCoeff(&wire, termToCompute)
	}

	solution.set(vID, wire)

	return nil
}

// TODO @gbotrel clean logs and html see https://github.com/ConsenSys/gnark/issues/140

// ToHTML returns an HTML human-readable representation of the constraint system
func (cs *R1CS) ToHTML(w io.Writer) error {
	t, err := template.New("cs.html").Funcs(template.FuncMap{
		"toHTML": toHTML,
		"add":    add,
		"sub":    sub,
	}).Parse(compiled.R1CSTemplate)
	if err != nil {
		return err
	}

	return t.Execute(w, cs)
}

func add(a, b int) int {
	return a + b
}

func sub(a, b int) int {
	return a - b
}

func toHTML(l compiled.Variable, coeffs []fr.Element, MHints map[int]compiled.Hint) string {
	var sbb strings.Builder
	for i := 0; i < len(l.LinExp); i++ {
		termToHTML(l.LinExp[i], &sbb, coeffs, MHints, false)
		if i+1 < len(l.LinExp) {
			sbb.WriteString(" + ")
		}
	}
	return sbb.String()
}

func termToHTML(t compiled.Term, sbb *strings.Builder, coeffs []fr.Element, MHints map[int]compiled.Hint, offset bool) {
	tID := t.CoeffID()
	if tID == compiled.CoeffIdOne {
		// do nothing, just print the variable
	} else if tID == compiled.CoeffIdMinusOne {
		// print neg sign
		sbb.WriteString("<span class=\"coefficient\">-</span>")
	} else if tID == compiled.CoeffIdZero {
		sbb.WriteString("<span class=\"coefficient\">0</span>")
		return
	} else {
		sbb.WriteString("<span class=\"coefficient\">")
		sbb.WriteString(coeffs[tID].String())
		sbb.WriteString("</span>*")
	}

	vID := t.VariableID()
	class := ""
	switch t.VariableVisibility() {
	case compiled.Internal:
		class = "internal"
		if _, ok := MHints[vID]; ok {
			class = "hint"
		}
	case compiled.Public:
		class = "public"
	case compiled.Secret:
		class = "secret"
	case compiled.Virtual:
		class = "virtual"
	case compiled.Unset:
		class = "unset"
	default:
		panic("not implemented")
	}
	if offset {
		vID++ // for sparse R1CS, we offset to have same variable numbers as in R1CS
	}
	sbb.WriteString(fmt.Sprintf("<span class=\"%s\">v%d</span>", class, vID))

}

// GetNbCoefficients return the number of unique coefficients needed in the R1CS
func (cs *R1CS) GetNbCoefficients() int {
	return len(cs.Coefficients)
}

// CurveID returns curve ID as defined in gnark-crypto (ecc.BW6-761)
func (cs *R1CS) CurveID() ecc.ID {
	return ecc.BW6_761
}

// FrSize return fr.Limbs * 8, size in byte of a fr element
func (cs *R1CS) FrSize() int {
	return fr.Limbs * 8
}

// WriteTo encodes R1CS into provided io.Writer using cbor
func (cs *R1CS) WriteTo(w io.Writer) (int64, error) {
	_w := ioutils.WriterCounter{W: w} // wraps writer to count the bytes written
	enc, err := cbor.CoreDetEncOptions().EncMode()
	if err != nil {
		return 0, err
	}
	encoder := enc.NewEncoder(&_w)

	// encode our object
	err = encoder.Encode(cs)
	return _w.N, err
}

// ReadFrom attempts to decode R1CS from io.Reader using cbor
func (cs *R1CS) ReadFrom(r io.Reader) (int64, error) {
	dm, err := cbor.DecOptions{
		MaxArrayElements: 134217728,
		MaxMapPairs:      134217728,
	}.DecMode()

	if err != nil {
		return 0, err
	}
	decoder := dm.NewDecoder(r)
	if err := decoder.Decode(&cs); err != nil {
		return int64(decoder.NumBytesRead()), err
	}

	return int64(decoder.NumBytesRead()), nil
}
