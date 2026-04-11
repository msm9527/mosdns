package server

import (
	"errors"
	"net"

	"go.uber.org/zap"
)

var (
	errListenerCtxCanceled   = errors.New("listener ctx canceled")
	errConnectionCtxCanceled = errors.New("connection ctx canceled")
)

var (
	nopLogger = zap.NewNop()
)

func isListenerCloseErr(err error) bool {
	return errors.Is(err, net.ErrClosed)
}
