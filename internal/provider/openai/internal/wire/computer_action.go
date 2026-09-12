package wire

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/deyna256/clan/internal/generation"
)

type computerPoint struct {
	X generation.Optional[int64] `json:"x,omitzero"`
	Y generation.Optional[int64] `json:"y,omitzero"`
}
type computerAction struct {
	Type    string                        `json:"type"`
	Button  string                        `json:"button,omitempty"`
	X       generation.Optional[int64]    `json:"x,omitzero"`
	Y       generation.Optional[int64]    `json:"y,omitzero"`
	ScrollX generation.Optional[int64]    `json:"scroll_x,omitzero"`
	ScrollY generation.Optional[int64]    `json:"scroll_y,omitzero"`
	Keys    generation.Optional[[]string] `json:"keys,omitzero"`
	Path    []computerPoint               `json:"path,omitzero"`
	Text    generation.Optional[string]   `json:"text,omitzero"`
}

func encodeComputerAction(action generation.ComputerAction) (json.RawMessage, error) {
	var value computerAction
	switch action := action.(type) {
	case generation.ComputerClick:
		value.Type, value.X, value.Y, value.Keys, value.Button = "click", generation.Some(action.X), generation.Some(action.Y), action.Keys, action.Button
		if !computerButton(action.Button) {
			return nil, invalid("button", "unsupported mouse button")
		}
	case generation.ComputerDoubleClick:
		value.Type, value.X, value.Y, value.Keys = "double_click", generation.Some(action.X), generation.Some(action.Y), action.Keys
		if action.Keys.IsZero() {
			return nil, invalid("keys", "double click requires keys or null")
		}
	case generation.ComputerDrag:
		value.Type, value.Keys = "drag", action.Keys
		value.Path = make([]computerPoint, len(action.Path))
		for i, point := range action.Path {
			value.Path[i] = computerPoint{generation.Some(point.X), generation.Some(point.Y)}
		}
	case generation.ComputerKeypress:
		value.Type, value.Keys = "keypress", generation.Some(action.Keys)
	case generation.ComputerMove:
		value.Type, value.X, value.Y, value.Keys = "move", generation.Some(action.X), generation.Some(action.Y), action.Keys
	case generation.ComputerScreenshot:
		value.Type = "screenshot"
	case generation.ComputerScroll:
		value.Type, value.X, value.Y, value.Keys = "scroll", generation.Some(action.X), generation.Some(action.Y), action.Keys
		value.ScrollX, value.ScrollY = generation.Some(action.ScrollX), generation.Some(action.ScrollY)
	case generation.ComputerType:
		if !utf8.ValidString(action.Text) {
			return nil, invalid("text", "must be valid UTF-8")
		}
		value.Type, value.Text = "type", generation.Some(action.Text)
	case generation.ComputerWait:
		value.Type = "wait"
	default:
		return nil, invalid("", "expected a computer action")
	}
	if keys, ok := value.Keys.Value(); ok {
		if keys == nil {
			value.Keys = generation.Some([]string{})
		}
		for _, key := range keys {
			if !utf8.ValidString(key) {
				return nil, invalid("keys", "must be valid UTF-8")
			}
		}
	}
	return json.Marshal(value)
}

func computerButton(button string) bool {
	switch button {
	case "left", "right", "wheel", "back", "forward":
		return true
	}
	return false
}

func decodeComputerAction(raw json.RawMessage) (generation.ComputerAction, error) {
	var decoded struct {
		computerAction
		Keys generation.Optional[strictStrings] `json:"keys"`
	}
	if absent(raw) || !utf8.Valid(raw) || json.Unmarshal(raw, &decoded) != nil || decoded.Type == "" {
		return nil, failure(generation.ProtocolError)
	}
	value := decoded.computerAction
	if decoded.Keys.IsNull() {
		value.Keys = generation.Null[[]string]()
	}
	if keys, ok := decoded.Keys.Value(); ok {
		value.Keys = generation.Some([]string(keys))
	}
	x, hasX := value.X.Value()
	y, hasY := value.Y.Value()
	switch value.Type {
	case "click", "double_click", "move", "scroll":
		if !hasX || !hasY {
			return nil, failure(generation.ProtocolError)
		}
	}
	switch value.Type {
	case "click":
		if !computerButton(value.Button) {
			return nil, failure(generation.ProtocolError)
		}
		return generation.ComputerClick{X: x, Y: y, Button: value.Button, Keys: value.Keys}, nil
	case "double_click":
		if value.Keys.IsZero() {
			return nil, failure(generation.ProtocolError)
		}
		return generation.ComputerDoubleClick{X: x, Y: y, Keys: value.Keys}, nil
	case "move":
		return generation.ComputerMove{X: x, Y: y, Keys: value.Keys}, nil
	case "scroll":
		scrollX, hasScrollX := value.ScrollX.Value()
		scrollY, hasScrollY := value.ScrollY.Value()
		if !hasScrollX || !hasScrollY {
			return nil, failure(generation.ProtocolError)
		}
		return generation.ComputerScroll{X: x, Y: y, ScrollX: scrollX, ScrollY: scrollY, Keys: value.Keys}, nil
	case "drag":
		if value.Path == nil {
			return nil, failure(generation.ProtocolError)
		}
		path := make([]generation.ComputerPoint, len(value.Path))
		for i, point := range value.Path {
			px, hasPX := point.X.Value()
			py, hasPY := point.Y.Value()
			if !hasPX || !hasPY {
				return nil, failure(generation.ProtocolError)
			}
			path[i] = generation.ComputerPoint{X: px, Y: py}
		}
		return generation.ComputerDrag{Path: path, Keys: value.Keys}, nil
	case "keypress":
		keys, ok := value.Keys.Value()
		if !ok {
			return nil, failure(generation.ProtocolError)
		}
		return generation.ComputerKeypress{Keys: keys}, nil
	case "type":
		text, ok := value.Text.Value()
		if !ok {
			return nil, failure(generation.ProtocolError)
		}
		return generation.ComputerType{Text: text}, nil
	case "screenshot":
		return generation.ComputerScreenshot{}, nil
	case "wait":
		return generation.ComputerWait{}, nil
	default:
		return nil, failure(generation.Unsupported)
	}
}
