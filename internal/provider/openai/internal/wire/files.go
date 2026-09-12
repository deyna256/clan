package wire

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/deyna256/clan/internal/generation"
)

func encodeFile(part generation.Part, output bool) (json.RawMessage, error) {
	if output {
		return nil, invalid("", "files cannot be assistant output parts")
	}
	var body struct {
		Type       string                      `json:"type"`
		ID         string                      `json:"file_id,omitempty"`
		URL        string                      `json:"file_url,omitempty"`
		Data       string                      `json:"file_data,omitempty"`
		Filename   generation.Optional[string] `json:"filename,omitzero"`
		Detail     string                      `json:"detail,omitempty"`
		Breakpoint json.RawMessage             `json:"prompt_cache_breakpoint,omitempty"`
	}
	body.Type = "input_file"
	var options generation.FileOptions
	switch file := part.(type) {
	case generation.FileID:
		if err := requiredString("file_id", file.ID); err != nil {
			return nil, err
		}
		body.ID, options = file.ID, file.Options
	case generation.FileURL:
		if !httpURL(file.URL) {
			return nil, invalid("file_url", "expected an HTTP(S) URL")
		}
		body.URL, options = file.URL, file.Options
	case generation.FileData:
		if !fileData(file.Data) {
			return nil, invalid("file_data", "expected base64 file data or a base64 data URL")
		}
		body.Data, options = file.Data, file.Options
	default:
		return nil, invalid("", "expected a file source")
	}
	if name, ok := options.Filename.Value(); options.Filename.IsNull() || (ok && !utf8.ValidString(name)) {
		return nil, invalid("filename", "must be valid UTF-8")
	}
	switch options.Detail {
	case "", "auto", "low", "high":
	default:
		return nil, invalid("detail", "unsupported file detail")
	}
	body.Filename, body.Detail = options.Filename, options.Detail
	if options.OpenAI.PromptCacheBreakpoint {
		body.Breakpoint = json.RawMessage(`{"mode":"explicit"}`)
	}
	return json.Marshal(body)
}

func httpURL(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	u, err := url.Parse(value)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil
}

func fileData(value string) bool {
	if strings.HasPrefix(value, "data:") {
		header, data, ok := strings.Cut(value, ",")
		if !ok || !strings.HasSuffix(header, ";base64") || !utf8.ValidString(header) {
			return false
		}
		value = data
	}
	return ValidBase64Data(value)
}

// ValidBase64Data reports whether value encodes nonempty bytes.
func ValidBase64Data(value string) bool {
	decoded, err := base64.StdEncoding.DecodeString(value)
	return err == nil && len(decoded) != 0
}
