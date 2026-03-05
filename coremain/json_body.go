package coremain

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

var (
	errJSONBodyTooLarge   = errors.New("request body too large")
	errJSONBodyExtraValue = errors.New("request body must contain only one JSON value")
)

func decodeJSONBodyStrict(w http.ResponseWriter, r *http.Request, dst any, allowEmpty bool) error {
	if r == nil || r.Body == nil || r.Body == http.NoBody {
		if allowEmpty {
			return nil
		}
		return io.EOF
	}

	limitJSONBody(w, r)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) && allowEmpty {
			return nil
		}
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return fmt.Errorf("%w: max %d bytes", errJSONBodyTooLarge, maxBytesErr.Limit)
		}
		return err
	}

	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return fmt.Errorf("%w: max %d bytes", errJSONBodyTooLarge, maxBytesErr.Limit)
		}
		if err == nil {
			return errJSONBodyExtraValue
		}
		return fmt.Errorf("%w: %v", errJSONBodyExtraValue, err)
	}

	return nil
}
