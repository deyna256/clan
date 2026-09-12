package wire

import (
	"cmp"
	"encoding/json"
	"slices"
	"unicode/utf8"

	"github.com/deyna256/clan/internal/generation"
)

type citation struct {
	Type        string  `json:"type"`
	URL         *string `json:"url,omitempty"`
	Title       *string `json:"title,omitempty"`
	FileID      *string `json:"file_id,omitempty"`
	Filename    *string `json:"filename,omitempty"`
	ContainerID *string `json:"container_id,omitempty"`
	Index       *int64  `json:"index,omitempty"`
	Start       *int64  `json:"start_index,omitempty"`
	End         *int64  `json:"end_index,omitempty"`
}

func (c citation) value() (generation.Citation, error) {
	validRange := c.Start != nil && c.End != nil && *c.Start >= 0 && *c.End >= *c.Start
	validIndex := c.Index != nil && *c.Index >= 0
	switch c.Type {
	case "url_citation":
		if c.URL != nil && c.Title != nil && validRange {
			return generation.URLCitation{URL: *c.URL, Title: *c.Title, Start: *c.Start, End: *c.End}, nil
		}
	case "file_citation":
		if c.FileID != nil && c.Filename != nil && validIndex {
			return generation.FileCitation{FileID: *c.FileID, Filename: *c.Filename, Index: *c.Index}, nil
		}
	case "container_file_citation":
		if c.ContainerID != nil && c.FileID != nil && c.Filename != nil && validRange {
			return generation.ContainerFileCitation{ContainerID: *c.ContainerID, FileID: *c.FileID, Filename: *c.Filename, Start: *c.Start, End: *c.End}, nil
		}
	case "file_path":
		if c.FileID != nil && validIndex {
			return generation.FilePath{FileID: *c.FileID, Index: *c.Index}, nil
		}
	default:
		return nil, failure(generation.Unsupported)
	}
	return nil, failure(generation.ProtocolError)
}

func DecodeAnnotation(raw json.RawMessage, index int) (generation.Annotation, error) {
	var c citation
	if index < 0 || !utf8.Valid(raw) || json.Unmarshal(raw, &c) != nil {
		return generation.Annotation{}, failure(generation.ProtocolError)
	}
	value, err := c.value()
	return generation.Annotation{Index: index, Citation: value}, err
}

func encodeAnnotation(annotation generation.Annotation) (json.RawMessage, error) {
	var c citation
	switch v := annotation.Citation.(type) {
	case generation.URLCitation:
		c = citation{Type: "url_citation", URL: &v.URL, Title: &v.Title, Start: &v.Start, End: &v.End}
	case generation.FileCitation:
		c = citation{Type: "file_citation", FileID: &v.FileID, Filename: &v.Filename, Index: &v.Index}
	case generation.ContainerFileCitation:
		c = citation{Type: "container_file_citation", ContainerID: &v.ContainerID, FileID: &v.FileID, Filename: &v.Filename, Start: &v.Start, End: &v.End}
	case generation.FilePath:
		c = citation{Type: "file_path", FileID: &v.FileID, Index: &v.Index}
	default:
		return nil, invalid("annotations", "unsupported citation value")
	}
	if _, err := c.value(); err != nil {
		return nil, invalid("annotations", "invalid citation offsets")
	}
	for _, value := range []*string{c.URL, c.Title, c.FileID, c.Filename, c.ContainerID} {
		if value != nil && !utf8.ValidString(*value) {
			return nil, invalid("annotations", "must be valid UTF-8")
		}
	}
	return json.Marshal(c)
}

type tokenProbability struct {
	Token   *string  `json:"token"`
	Bytes   []int    `json:"bytes"`
	Logprob *float64 `json:"logprob"`
}
type tokenLogprob struct {
	tokenProbability
	Top []tokenProbability `json:"top_logprobs"`
}

func (p tokenProbability) value() (generation.TokenProbability, error) {
	if p.Token == nil || p.Logprob == nil || *p.Logprob > 0 || !finite(*p.Logprob) {
		return generation.TokenProbability{}, failure(generation.ProtocolError)
	}
	var data []byte
	if p.Bytes != nil {
		data = make([]byte, len(p.Bytes))
	}
	for i, b := range p.Bytes {
		if b < 0 || b > 255 {
			return generation.TokenProbability{}, failure(generation.ProtocolError)
		}
		data[i] = byte(b)
	}
	return generation.TokenProbability{Token: *p.Token, Bytes: data, Logprob: *p.Logprob}, nil
}

func DecodeLogprobs(raw json.RawMessage) ([]generation.TokenLogprob, error) {
	if absent(raw) {
		return nil, nil
	}
	var records []tokenLogprob
	if !utf8.Valid(raw) || json.Unmarshal(raw, &records) != nil {
		return nil, failure(generation.ProtocolError)
	}
	var result []generation.TokenLogprob
	for _, record := range records {
		value, err := record.value()
		if err != nil {
			return nil, err
		}
		token := generation.TokenLogprob{TokenProbability: value}
		for _, candidate := range record.Top {
			value, err := candidate.value()
			if err != nil {
				return nil, err
			}
			token.Top = append(token.Top, value)
		}
		result = append(result, token)
	}
	return result, nil
}

func encodeProbability(p generation.TokenProbability) (tokenProbability, error) {
	if !utf8.ValidString(p.Token) || p.Logprob > 0 || !finite(p.Logprob) {
		return tokenProbability{}, invalid("logprobs", "expected UTF-8 tokens and finite nonpositive probabilities")
	}
	var data []int
	if p.Bytes != nil {
		data = make([]int, len(p.Bytes))
	}
	for i, b := range p.Bytes {
		data[i] = int(b)
	}
	return tokenProbability{Token: &p.Token, Bytes: data, Logprob: &p.Logprob}, nil
}

func encodeText(text generation.Text) (json.RawMessage, error) {
	annotations := make([]json.RawMessage, 0, len(text.OpenAI.Annotations))
	ordered := slices.Clone(text.OpenAI.Annotations)
	slices.SortFunc(ordered, func(a, b generation.Annotation) int {
		return cmp.Compare(a.Index, b.Index)
	})
	for i, annotation := range ordered {
		if annotation.Index < 0 || (i > 0 && ordered[i-1].Index == annotation.Index) {
			return nil, invalid("annotations", "indices must be nonnegative and distinct")
		}
		raw, err := encodeAnnotation(annotation)
		if err != nil {
			return nil, err
		}
		annotations = append(annotations, raw)
	}
	var probabilities []tokenLogprob
	for _, record := range text.OpenAI.Logprobs {
		p, err := encodeProbability(record.TokenProbability)
		if err != nil {
			return nil, err
		}
		encoded := tokenLogprob{tokenProbability: p, Top: make([]tokenProbability, 0, len(record.Top))}
		for _, top := range record.Top {
			p, err := encodeProbability(top)
			if err != nil {
				return nil, err
			}
			encoded.Top = append(encoded.Top, p)
		}
		probabilities = append(probabilities, encoded)
	}
	return json.Marshal(struct {
		Type        string            `json:"type"`
		Text        string            `json:"text"`
		Annotations []json.RawMessage `json:"annotations"`
		Logprobs    []tokenLogprob    `json:"logprobs,omitempty"`
	}{"output_text", text.Text, annotations, probabilities})
}

func decodeTextData(annotations, logprobs json.RawMessage, warn func(string)) generation.OpenAITextData {
	var result generation.OpenAITextData
	var records []json.RawMessage
	if !absent(annotations) && json.Unmarshal(annotations, &records) != nil {
		warn("invalid_annotation")
	}
	for index, raw := range records {
		annotation, err := DecodeAnnotation(raw, index)
		if err != nil {
			warn("invalid_annotation")
			continue
		}
		result.Annotations = append(result.Annotations, annotation)
	}
	var err error
	result.Logprobs, err = DecodeLogprobs(logprobs)
	if err != nil {
		warn("invalid_logprobs")
	}
	return result
}
