// Copyright 2020 ConsenSys AG
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

// Code generated by gnark/internal/generators DO NOT EDIT

package backend

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/consensys/gnark/backend"
	"github.com/consensys/gnark/backend/r1cs/r1c"
	"github.com/consensys/gurvy"

	"github.com/consensys/gurvy/bls377/fr"
)

// R1CS decsribes a set of R1CS constraint
type R1CS struct {
	// Wires
	NbWires       int
	NbPublicWires int // includes ONE wire
	NbSecretWires int
	SecretWires   []string         // private wire names, correctly ordered (the i-th entry is the name of the (offset+)i-th wire)
	PublicWires   []string         // public wire names, correctly ordered (the i-th entry is the name of the (offset+)i-th wire)
	WireTags      map[int][]string // optional tags -- debug info

	// Constraints
	NbConstraints   int // total number of constraints
	NbCOConstraints int // number of constraints that need to be solved, the first of the Constraints slice
	Constraints     []r1c.R1C
	Coefficients    []fr.Element // R1C coefficients indexes point here
}

// GetNbConstraints returns the number of constraints
func (r1cs *R1CS) GetNbConstraints() int {
	return r1cs.NbConstraints
}

// GetNbWires returns the number of wires
func (r1cs *R1CS) GetNbWires() int {
	return r1cs.NbWires
}

// GetNbCoefficients return the number of (different) coefficients needed in the R1CS
func (r1cs *R1CS) GetNbCoefficients() int {
	return len(r1cs.Coefficients)
}

// GetCurveID returns the curveID of this R1CS
func (r1cs *R1CS) GetCurveID() gurvy.ID {
	return gurvy.BLS377
}

// Solve sets all the wires and returns the a, b, c vectors.
// the r1cs system should have been compiled before. The entries in a, b, c are in Montgomery form.
// assignment: map[string]value: contains the input variables
// TODO : note that currently, there is a convertion from interface{} to fr.Element for each entry in the
// assignment map. It can cost a SetBigInt() which converts from Regular ton Montgomery rep (1 mul)
// while it's unlikely to be noticeable compared to the FFT and the MultiExp compute times,
// there should be a faster (statically typed) path for production deployments.
// a, b, c vectors: ab-c = hz
// wireValues =  [intermediateVariables | privateInputs | publicInputs]
func (r1cs *R1CS) Solve(assignment map[string]interface{}, a, b, c, wireValues []fr.Element) error {
	// compute the wires and the a, b, c polynomials
	if len(a) == r1cs.NbConstraints || len(b) == r1cs.NbConstraints || len(c) == r1cs.NbConstraints || len(wireValues) == r1cs.NbWires {
		return errors.New("invalid input size: len(a, b, c) == r1cs.NbConstraints and len(wireValues) == r1cs.NbWires")
	}

	// keep track of wire that have a value
	wireInstantiated := make([]bool, r1cs.NbWires)

	// instantiate the public/ private inputs
	instantiateInputs := func(offset int, inputNames []string) error {
		for i := 0; i < len(inputNames); i++ {
			name := inputNames[i]
			if name == backend.OneWire {
				wireValues[i+offset].SetOne()
				wireInstantiated[i+offset] = true
			} else {
				if val, ok := assignment[name]; ok {
					wireValues[i+offset].SetInterface(val)
					wireInstantiated[i+offset] = true
				} else {
					return fmt.Errorf("%q: %w", name, backend.ErrInputNotSet)
				}
			}
		}
		return nil
	}
	// instantiate private inputs
	if r1cs.NbSecretWires != 0 {
		offset := r1cs.NbWires - r1cs.NbPublicWires - r1cs.NbSecretWires // private input start index
		if err := instantiateInputs(offset, r1cs.SecretWires); err != nil {
			return err
		}
	}
	// instantiate public inputs
	{
		offset := r1cs.NbWires - r1cs.NbPublicWires // public input start index
		if err := instantiateInputs(offset, r1cs.PublicWires); err != nil {
			return err
		}
	}

	// check if there is an inconsistant constraint
	var check fr.Element

	// Loop through the other Constraints
	for i, r1c := range r1cs.Constraints {

		if i < r1cs.NbCOConstraints {
			// computationalGraph : we need to solve the constraint
			// computationalGraph[i] contains exactly one uncomputed wire (due
			// to the graph being correctly ordered), we solve it
			solveR1C(&r1cs.Constraints[i], r1cs, wireInstantiated, wireValues)
		}

		// A this stage we are not guaranteed that a[i+sizecg]*b[i+sizecg]=c[i+sizecg] because we only query the values (computed
		// at the previous step)
		a[i], b[i], c[i] = instantiateR1C(&r1c, r1cs, wireValues)

		// check that the constraint is satisfied
		check.Mul(&a[i], &b[i])
		if !check.Equal(&c[i]) {
			invalidA := a[i]
			invalidB := b[i]
			invalidC := c[i]

			return fmt.Errorf("%w: %q * %q != %q", backend.ErrUnsatisfiedConstraint,
				invalidA.String(),
				invalidB.String(),
				invalidC.String())
		}
	}

	return nil
}

// Inspect returns the tagged variables with their corresponding value
// If showsInput is set, it also puts in the resulting map the inputs (public and private).
// this is temporary while we refactor map[string]interface{} and use big.Int here.
func (r1cs *R1CS) Inspect(solution map[string]interface{}, showsInputs bool) (map[string]interface{}, error) {
	res := make(map[string]interface{})

	wireValues := make([]fr.Element, r1cs.NbWires)
	a := make([]fr.Element, r1cs.NbConstraints)
	b := make([]fr.Element, r1cs.NbConstraints)
	c := make([]fr.Element, r1cs.NbConstraints)

	err := r1cs.Solve(solution, a, b, c, wireValues)

	// showsInput is set, put the inputs in the resulting map
	if showsInputs {
		offset := r1cs.NbWires - r1cs.NbPublicWires - r1cs.NbSecretWires // private input start index
		for i := 0; i < len(r1cs.SecretWires); i++ {
			v := new(big.Int)
			res[r1cs.SecretWires[i]] = *(wireValues[i+offset].ToBigIntRegular(v))
		}
		offset = r1cs.NbWires - r1cs.NbPublicWires // public input start index
		for i := 0; i < len(r1cs.PublicWires); i++ {
			v := new(big.Int)
			res[r1cs.PublicWires[i]] = *(wireValues[i+offset].ToBigIntRegular(v))
		}
	}

	// get the tagged variables
	for wireID, tags := range r1cs.WireTags {
		for _, tag := range tags {
			if _, ok := res[tag]; ok {
				return nil, errors.New("duplicate tag: " + tag)
			}
			v := new(big.Int)
			res[tag] = *(wireValues[wireID].ToBigIntRegular(v))
		}

	}

	// the error cannot be caught before because the res map needs to be filled
	if err != nil {
		return res, err
	}

	return res, nil
}

// MulAdd returns accumulator += (value * term.Coefficient)
func MulAdd(t r1c.Term, r1cs *R1CS, buffer, value, accumulator *fr.Element) {
	specialValue := t.SpecialValueInt()
	switch specialValue {
	case 1:
		accumulator.Add(accumulator, value)
	case -1:
		accumulator.Sub(accumulator, value)
	case 0:
		return
	case 2:
		buffer.Double(value)
		accumulator.Add(accumulator, buffer)
	default:
		buffer.Mul(&r1cs.Coefficients[t.CoeffID()], value)
		accumulator.Add(accumulator, buffer)
	}
}

// mulInto returns into.Mul(into, term.Coefficient)
func mulInto(t r1c.Term, r1cs *R1CS, into *fr.Element) *fr.Element {
	specialValue := t.SpecialValueInt()
	switch specialValue {
	case 1:
		return into
	case -1:
		return into.Neg(into)
	case 0:
		return into.SetZero()
	case 2:
		return into.Double(into)
	default:
		return into.Mul(into, &r1cs.Coefficients[t.CoeffID()])
	}
}

// compute left, right, o part of a r1cs constraint
// this function is called when all the wires have been computed
// it instantiates the l, r o part of a R1C
func instantiateR1C(r *r1c.R1C, r1cs *R1CS, wireValues []fr.Element) (a, b, c fr.Element) {

	var tmp fr.Element

	for _, t := range r.L {
		MulAdd(t, r1cs, &tmp, &wireValues[t.ConstraintID()], &a)
	}

	for _, t := range r.R {
		MulAdd(t, r1cs, &tmp, &wireValues[t.ConstraintID()], &b)
	}

	for _, t := range r.O {
		MulAdd(t, r1cs, &tmp, &wireValues[t.ConstraintID()], &c)
	}

	return
}

type location uint8

const (
	locationUnset location = iota
	locationA
	locationB
	locationC
)

func (l location) set(nloc location) location {
	if l != locationUnset {
		panic("location was already set -- we should have only one unset wire")
	}
	return nloc
}

// solveR1c computes a wire by solving a r1cs
// the function searches for the unset wire (either the unset wire is
// alone, or it can be computed without ambiguity using the other computed wires
// , eg when doing a binary decomposition: either way the missing wire can
// be computed without ambiguity because the r1cs is correctly ordered)
func solveR1C(r *r1c.R1C, r1cs *R1CS, wireInstantiated []bool, wireValues []fr.Element) {

	switch r.Solver {

	// in this case we solve a R1C by isolating the uncomputed wire
	case r1c.SingleOutput:

		// the index of the non zero entry shows if L, R or O has an uninstantiated wire
		// the content is the ID of the wire non instantiated
		loc := locationUnset

		var tmp, a, b, c fr.Element
		var _t r1c.Term

		for _, t := range r.L {
			cID := t.ConstraintID()
			if wireInstantiated[cID] {
				MulAdd(t, r1cs, &tmp, &wireValues[cID], &a)
			} else {
				_t = t
				loc = loc.set(locationA)
			}
		}

		for _, t := range r.R {
			cID := t.ConstraintID()
			if wireInstantiated[cID] {
				MulAdd(t, r1cs, &tmp, &wireValues[cID], &b)
			} else {
				_t = t
				loc = loc.set(locationB)
			}
		}

		for _, t := range r.O {
			cID := t.ConstraintID()
			if wireInstantiated[cID] {
				MulAdd(t, r1cs, &tmp, &wireValues[cID], &c)
			} else {
				_t = t
				loc = loc.set(locationC)
			}
		}

		// ensure we found the unset wire
		if loc == locationUnset {
			// this wire may have been instantiated as part of moExpression already
			return
		}

		cID := _t.ConstraintID()
		wireValues[cID].SetZero()
		wireInstantiated[cID] = true

		switch loc {
		case locationA:
			if !b.IsZero() {
				wireValues[cID].Div(&c, &b).
					Sub(&wireValues[cID], &a)
				mulInto(_t, r1cs, &wireValues[cID])
			}
		case locationB:
			if !a.IsZero() {
				wireValues[cID].Div(&c, &a).
					Sub(&wireValues[cID], &b)
				mulInto(_t, r1cs, &wireValues[cID])
			}
		case locationC:
			wireValues[cID].Mul(&a, &b).
				Sub(&wireValues[cID], &c)
			mulInto(_t, r1cs, &wireValues[cID])
		}

	// in the case the R1C is solved by directly computing the binary decomposition
	// of the variable
	case r1c.BinaryDec:

		// the binary decomposition must be called on the non Mont form of the number
		n := wireValues[r.O[0].ConstraintID()].ToRegular()
		nbBits := len(r.L)

		// binary decomposition of n
		var i, j int
		for i*64 < nbBits {
			j = 0
			for j < 64 && i*64+j < len(r.L) {
				ithbit := (n[i] >> uint(j)) & 1
				cID := r.L[i*64+j].ConstraintID()
				if !wireInstantiated[cID] {
					wireValues[cID].SetUint64(ithbit)
					wireInstantiated[cID] = true
				}
				j++
			}
			i++
		}
	default:
		panic("unimplemented solving method")
	}
}
