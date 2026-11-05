// Package awserr tells aws errors apart by their code, the one way every package here does it.
package awserr

import (
	"errors"
	"slices"

	"github.com/aws/smithy-go"
)

// Code returns the error code aws answered with, such as ResourceNotFoundException, or "" when
// err is not an answer from aws.
func Code(err error) string {
	if api, ok := errors.AsType[smithy.APIError](err); ok {
		return api.ErrorCode()
	}
	return ""
}

// Is reports whether aws answered with one of codes.
func Is(err error, codes ...string) bool {
	return err != nil && slices.Contains(codes, Code(err))
}
