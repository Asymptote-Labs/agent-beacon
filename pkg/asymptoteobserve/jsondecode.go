package asymptoteobserve

import (
	"encoding/json"
	"errors"
	"io"
)

// DecodeOnly decodes one value from dec into v and requires the input to end after it, apart
// from whitespace. Configure dec (UseNumber, DisallowUnknownFields) before calling.
//
// Decoder.More is not that check: it reports false in front of a stray "]" or "}", so a value
// followed by one, or by "] {...}", would pass.
func DecodeOnly(dec *json.Decoder, v any) error {
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("trailing data after the JSON value")
	}
	return nil
}
