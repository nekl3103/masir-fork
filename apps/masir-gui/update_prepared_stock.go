//go:build !mygo_patched

package main

import (
	"context"
	"errors"
	"github.com/egoist/mygo"
)

func preparedUpdateSupported() bool { return false }
func installPreparedUpdate(context.Context, *mygo.Update, func(int64, int64), func(string) error) error {
	return errors.New(tr("prepared updates require the patched desktop build"))
}
