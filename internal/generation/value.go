// Package generation defines the values exchanged by protocol adapters.
package generation

import (
	"bytes"
	"encoding/json"
)

// Optional distinguishes omission, explicit null and a value, including zero.
// Its zero value is omitted when encoded with encoding/json's omitzero option.
type Optional[T any] struct {
	value T
	state uint8
}

func Some[T any](value T) Optional[T] { return Optional[T]{value: value, state: 1} }
func Null[T any]() Optional[T]        { return Optional[T]{state: 2} }

func (o Optional[T]) Value() (T, bool) { return o.value, o.state == 1 }
func (o Optional[T]) IsZero() bool     { return o.state == 0 }
func (o Optional[T]) IsNull() bool     { return o.state == 2 }

func (o Optional[T]) MarshalJSON() ([]byte, error) {
	if o.state != 1 {
		return []byte("null"), nil
	}
	return json.Marshal(o.value)
}

func (o *Optional[T]) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		*o = Null[T]()
		return nil
	}
	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*o = Some(value)
	return nil
}
