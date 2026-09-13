package codex

import "encoding/json"

func validateTools(raw json.RawMessage) (json.RawMessage, error) {
	tools, err := array(raw, "tools")
	if err != nil {
		return nil, err
	}
	for i, tool := range tools {
		fields, err := object(tool, "tools", "type name description parameters strict")
		if err != nil {
			return nil, err
		}
		if _, err = enumValue(fields["type"], "tools.type", "function"); err != nil {
			return nil, err
		}
		omitNull(fields, "description parameters strict")
		if _, err = textValue(fields["name"], "tools.name", false); err != nil {
			return nil, err
		}
		if value, ok := fields["description"]; ok {
			if _, err = textValue(value, "tools.description", true); err != nil {
				return nil, err
			}
		}
		if value, ok := fields["parameters"]; ok {
			if _, err = object(value, "tools.parameters", ""); err != nil {
				return nil, err
			}
		}
		if value, ok := fields["strict"]; ok {
			if _, err = boolValue(value, "tools.strict"); err != nil {
				return nil, err
			}
		}
		tools[i], _ = json.Marshal(fields)
	}
	return json.Marshal(tools)
}

func (r *Request) validateReasoning(raw json.RawMessage) (json.RawMessage, error) {
	fields, err := object(raw, "reasoning", "effort summary")
	if err != nil {
		return nil, err
	}
	omitNull(fields, "effort summary")
	if value, ok := fields["effort"]; ok {
		r.effort, err = textValue(value, "reasoning.effort", false)
		if err != nil {
			return nil, err
		}
	}
	if value, ok := fields["summary"]; ok {
		if _, err = enumValue(value, "reasoning.summary", "auto concise detailed"); err != nil {
			return nil, err
		}
		r.summary = true
	}
	return json.Marshal(fields)
}

func (r *Request) validateText(raw json.RawMessage) (json.RawMessage, error) {
	fields, err := object(raw, "text", "verbosity format")
	if err != nil {
		return nil, err
	}
	omitNull(fields, "verbosity")
	if value, ok := fields["verbosity"]; ok {
		if _, err = enumValue(value, "text.verbosity", "low medium high"); err != nil {
			return nil, err
		}
		r.verbosity = true
	}
	if format, ok := fields["format"]; ok {
		fields["format"], err = validateFormat(format)
		if err != nil {
			return nil, err
		}
	}
	return json.Marshal(fields)
}

func validateFormat(raw json.RawMessage) (json.RawMessage, error) {
	format, err := object(raw, "text.format", "type name schema description strict")
	if err != nil {
		return nil, err
	}
	kind, err := enumValue(format["type"], "text.format.type", "text json_object json_schema")
	if err != nil {
		return nil, err
	}
	if kind != "json_schema" {
		if err = allowed(format, "text.format", "type"); err != nil {
			return nil, err
		}
		return raw, nil
	}
	omitNull(format, "strict")
	if _, err = textValue(format["name"], "text.format.name", false); err != nil {
		return nil, err
	}
	if _, err = object(format["schema"], "text.format.schema", ""); err != nil {
		return nil, err
	}
	if value, ok := format["description"]; ok {
		if _, err = textValue(value, "text.format.description", true); err != nil {
			return nil, err
		}
	}
	if value, ok := format["strict"]; ok {
		if _, err = boolValue(value, "text.format.strict"); err != nil {
			return nil, err
		}
	}
	return json.Marshal(format)
}
