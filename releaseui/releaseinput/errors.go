// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseinput

import "errors"

// ErrInvalidInput marks an error caused by operator-controlled process input.
// During preparation, releaseui shows these errors in the input form rather than as general errors.
var ErrInvalidInput = errors.New("invalid release input")

// InvalidInputError associates an input validation error with a bound field.
type InvalidInputError struct {
	Target  any
	Message string
}

// NewInvalidInputError creates a validation error for the field pointer registered in
// [Set]. A nil or unmatched target displays the message at the form level.
func NewInvalidInputError(target any, message string) *InvalidInputError {
	return &InvalidInputError{Target: target, Message: message}
}

func (e *InvalidInputError) Error() string {
	return e.Message
}

func (e *InvalidInputError) Unwrap() error {
	return ErrInvalidInput
}
