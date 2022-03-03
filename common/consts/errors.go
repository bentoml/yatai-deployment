package consts

import (
	"github.com/pkg/errors"
)

var (
	ErrNotFound      = errors.New("not found")
	ErrNoPermission  = errors.New("no permission")
	ErrEmptyData     = errors.New("data is nil")
	ErrNoImplemented = errors.New("no implemented")
	ErrTimeout       = errors.New("timeout")
)
