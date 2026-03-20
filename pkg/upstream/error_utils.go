package upstream

import (
	"context"
	"errors"
)

func IsContextCanceled(err error) bool {
	return errors.Is(err, context.Canceled)
}
