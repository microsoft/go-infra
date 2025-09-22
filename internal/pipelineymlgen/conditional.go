// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package pipelineymlgen

import "fmt"

// conditionalStateMachine tracks state for a chain of conditional expressions.
type conditionalStateMachine struct {
	// taken a conditional branch in current chain of conditional expressions,
	// or nil if there is no current chain.
	taken *bool
}

// reset due to something in the sequence/map interrupting the chain.
func (c *conditionalStateMachine) reset() {
	c.taken = nil
}

// shouldTake returns whether the given conditional expression should be taken,
// updating c as needed. If an error is returned, the state is not updated.
func (c *conditionalStateMachine) shouldTake(cond evalConditional) (bool, error) {
	switch cond := cond.(type) {
	case *evalIf:
		r, err := cond.satisfied()
		if err != nil {
			return false, err
		}
		c.taken = &r
		return *c.taken, nil
	case *evalElseIf:
		if c.taken == nil {
			return false, fmt.Errorf("inlineelseif without preceding inlineif")
		}
		if *c.taken {
			return false, nil
		}
		r, err := cond.satisfied()
		if err != nil {
			return false, err
		}
		c.taken = &r
		return *c.taken, nil
	case *evalElse:
		if c.taken == nil {
			return false, fmt.Errorf("inlineelse without preceding inlineif")
		}
		defer c.reset()
		if *c.taken {
			return false, nil
		}
		return true, nil
	default:
		return false, fmt.Errorf("unknown conditional type %T", cond)
	}
}
