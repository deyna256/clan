package codex

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

func (r *Request) validateInput(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) != 0 && raw[0] == '"' {
		text, err := textValue(raw, "input", true)
		if err != nil {
			return nil, err
		}
		return json.Marshal([]any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]string{"type": "input_text", "text": text}}}})
	}
	items, err := array(raw, "input")
	if err != nil {
		return nil, err
	}
	for i, item := range items {
		path := fmt.Sprintf("input[%d]", i)
		fields, err := object(item, path, "")
		if err != nil {
			return nil, err
		}
		kind := "message"
		if value, ok := fields["type"]; ok {
			kind, err = textValue(value, path+".type", false)
			if err != nil {
				return nil, err
			}
		}
		switch kind {
		case "message":
			if err = allowed(fields, path, "type id role content status phase"); err != nil {
				return nil, err
			}
			role, roleErr := enumValue(fields["role"], path+".role", "user assistant system developer")
			if roleErr != nil {
				return nil, roleErr
			}
			if role == "system" {
				fields["role"] = json.RawMessage(`"developer"`)
			}
			if err = r.validateContent(fields["content"], path+".content", true); err != nil {
				return nil, err
			}
			if value, ok := fields["phase"]; ok && string(value) != "null" {
				_, err = enumValue(value, path+".phase", "commentary final_answer")
			}
		case "function_call":
			if err = allowed(fields, path, "type id call_id name arguments status"); err != nil {
				return nil, err
			}
			for _, name := range []string{"call_id", "name", "arguments"} {
				_, e := textValue(fields[name], path+"."+name, name == "arguments")
				if e != nil {
					return nil, e
				}
			}
		case "function_call_output":
			if err = allowed(fields, path, "type id call_id output status"); err != nil {
				return nil, err
			}
			if _, err = textValue(fields["call_id"], path+".call_id", false); err != nil {
				return nil, err
			}
			err = r.validateContent(fields["output"], path+".output", false)
		case "reasoning":
			if err = allowed(fields, path, "type id summary encrypted_content content status"); err != nil {
				return nil, err
			}
			if value, ok := fields["encrypted_content"]; ok && string(value) != "null" {
				if _, err = textValue(value, path+".encrypted_content", true); err != nil {
					return nil, err
				}
			}
			if err = validateTextParts(fields["summary"], path+".summary", "summary_text"); err != nil {
				return nil, err
			}
			if value, ok := fields["content"]; ok && string(value) != "null" {
				err = validateTextParts(value, path+".content", "reasoning_text")
			}
		default:
			return nil, invalid(path + ".type")
		}
		if err != nil {
			return nil, err
		}
		if value, ok := fields["id"]; ok && !(kind == "function_call_output" && string(value) == "null") {
			if _, err = textValue(value, path+".id", false); err != nil {
				return nil, err
			}
		}
		if value, ok := fields["status"]; ok && !(kind != "message" && string(value) == "null") {
			if _, err = enumValue(value, path+".status", "in_progress completed incomplete"); err != nil {
				return nil, err
			}
		}
		items[i], err = json.Marshal(fields)
		if err != nil {
			return nil, err
		}
	}
	return json.Marshal(items)
}

func (r *Request) validateContent(raw json.RawMessage, path string, outputText bool) error {
	if len(raw) != 0 && raw[0] == '"' {
		_, err := textValue(raw, path, true)
		return err
	}
	parts, err := array(raw, path)
	if err != nil {
		return err
	}
	for _, part := range parts {
		fields, err := object(part, path, "")
		if err != nil {
			return err
		}
		kind, err := textValue(fields["type"], path+".type", false)
		if err != nil {
			return err
		}
		switch kind {
		case "input_text":
			if err = allowed(fields, path, "type text"); err != nil {
				return err
			}
			_, err = textValue(fields["text"], path+".text", true)
		case "output_text":
			if !outputText {
				return invalid(path + ".type")
			}
			if err = allowed(fields, path, "type text annotations logprobs"); err != nil {
				return err
			}
			if _, err = textValue(fields["text"], path+".text", true); err != nil {
				return err
			}
			for _, name := range []string{"annotations", "logprobs"} {
				if value, ok := fields[name]; ok {
					if name == "logprobs" && string(value) == "null" {
						continue
					}
					if _, err = array(value, path+"."+name); err != nil {
						return err
					}
				}
			}
		case "refusal":
			if !outputText {
				return invalid(path + ".type")
			}
			if err = allowed(fields, path, "type refusal"); err != nil {
				return err
			}
			_, err = textValue(fields["refusal"], path+".refusal", true)
		case "input_image":
			r.image = true
			if err = allowed(fields, path, "type image_url detail"); err != nil {
				return err
			}
			value, e := textValue(fields["image_url"], path+".image_url", false)
			if e != nil {
				return e
			}
			if !imageURL(value) {
				return invalid(path + ".image_url")
			}
			if detail, ok := fields["detail"]; ok {
				value, err = enumValue(detail, path+".detail", "auto low high original")
				if value == "original" {
					r.originalImage = true
				}
			}
		default:
			return invalid(path + ".type")
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func validateTextParts(raw json.RawMessage, path, kind string) error {
	parts, err := array(raw, path)
	if err != nil {
		return err
	}
	for _, part := range parts {
		fields, e := object(part, path, "type text")
		if e != nil {
			return e
		}
		if _, e = enumValue(fields["type"], path+".type", kind); e != nil {
			return e
		}
		if _, e = textValue(fields["text"], path+".text", true); e != nil {
			return e
		}
	}
	return nil
}

func imageURL(value string) bool {
	if strings.HasPrefix(value, "data:image/") {
		header, content, ok := strings.Cut(value, ",")
		if !ok || !strings.HasSuffix(header, ";base64") || content == "" {
			return false
		}
		_, err := base64.StdEncoding.DecodeString(content)
		return err == nil
	}
	u, err := url.Parse(value)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil
}
