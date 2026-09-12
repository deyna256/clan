package wire

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
	"unicode/utf8"

	"github.com/deyna256/clan/internal/generation"
)

func encodeShellTool(tool generation.OpenAIShellTool) (json.RawMessage, error) {
	if callers, ok := tool.AllowedCallers.Value(); ok {
		if err := allowedCallers(callers); err != nil {
			return nil, err
		}
		if callers == nil {
			tool.AllowedCallers = generation.Some([]string{})
		}
	}
	environment, err := encodeShellEnvironment(tool.Environment, true)
	if err != nil {
		return nil, at("environment", err)
	}
	return json.Marshal(struct {
		Type           string                        `json:"type"`
		AllowedCallers generation.Optional[[]string] `json:"allowed_callers,omitzero"`
		Environment    json.RawMessage               `json:"environment,omitempty"`
	}{"shell", tool.AllowedCallers, environment})
}

func encodeShellEnvironment(environment generation.Optional[generation.ShellEnvironment], tool bool) (json.RawMessage, error) {
	if environment.IsZero() {
		return nil, nil
	}
	if environment.IsNull() {
		return json.RawMessage(`null`), nil
	}
	value, _ := environment.Value()
	switch value := value.(type) {
	case generation.ShellLocalEnvironment:
		type skill struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Path        string `json:"path"`
		}
		var skills []skill
		if value.Skills != nil {
			skills = make([]skill, len(value.Skills))
		}
		for i, entry := range value.Skills {
			if requiredString("skills.name", entry.Name) != nil || requiredString("skills.path", entry.Path) != nil || !utf8.ValidString(entry.Description) {
				return nil, invalid("skills", "invalid local skill")
			}
			skills[i] = skill{entry.Name, entry.Description, entry.Path}
		}
		return json.Marshal(struct {
			Type   string  `json:"type"`
			Skills []skill `json:"skills,omitzero"`
		}{"local", skills})
	case generation.ShellContainerReference:
		if err := requiredString("container_id", value.ContainerID); err != nil {
			return nil, err
		}
		return json.Marshal(struct {
			Type        string `json:"type"`
			ContainerID string `json:"container_id"`
		}{"container_reference", value.ContainerID})
	case generation.ShellAutoContainer:
		if !tool {
			return nil, invalid("type", "auto containers are only tool environments")
		}
		if memory, ok := value.MemoryLimit.Value(); ok && memory != "1g" && memory != "4g" && memory != "16g" && memory != "64g" {
			return nil, invalid("memory_limit", "unsupported memory limit")
		}
		for _, id := range value.FileIDs {
			if err := requiredString("file_ids", id); err != nil {
				return nil, err
			}
		}
		policy, err := EncodeContainerNetwork(value.NetworkPolicy)
		if err != nil {
			return nil, at("network_policy", err)
		}
		var skills []json.RawMessage
		if value.Skills != nil {
			skills = make([]json.RawMessage, len(value.Skills))
		}
		for i, skill := range value.Skills {
			skills[i], err = EncodeShellSkill(skill)
			if err != nil {
				return nil, at("skills", err)
			}
		}
		return json.Marshal(struct {
			Type          string                      `json:"type"`
			FileIDs       []string                    `json:"file_ids,omitzero"`
			MemoryLimit   generation.Optional[string] `json:"memory_limit,omitzero"`
			NetworkPolicy json.RawMessage             `json:"network_policy,omitempty"`
			Skills        []json.RawMessage           `json:"skills,omitzero"`
		}{"container_auto", value.FileIDs, value.MemoryLimit, policy, skills})
	default:
		return nil, invalid("", "expected a shell environment")
	}
}

// EncodeShellSkill encodes a skill shared by shell tools and container creation.
func EncodeShellSkill(skill generation.ShellSkill) (json.RawMessage, error) {
	switch value := skill.(type) {
	case generation.ShellSkillReference:
		if err := requiredString("skill_id", value.SkillID); err != nil {
			return nil, err
		}
		if value.Version.IsNull() {
			return nil, invalid("version", "must not be null")
		}
		if version, ok := value.Version.Value(); ok && version != "latest" {
			number, err := strconv.ParseUint(version, 10, 64)
			if err != nil || number == 0 {
				return nil, invalid("version", "expected a positive version or latest")
			}
		}
		return json.Marshal(struct {
			Type    string                      `json:"type"`
			SkillID string                      `json:"skill_id"`
			Version generation.Optional[string] `json:"version,omitzero"`
		}{"skill_reference", value.SkillID, value.Version})
	case generation.ShellInlineSkill:
		if requiredString("name", value.Name) != nil || !utf8.ValidString(value.Description) {
			return nil, invalid("", "invalid inline skill")
		}
		if _, err := base64.StdEncoding.DecodeString(value.Data); err != nil || value.Data == "" {
			return nil, invalid("source.data", "expected a base64 skill bundle")
		}
		return json.Marshal(struct {
			Type        string `json:"type"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Source      struct {
				Type      string `json:"type"`
				MediaType string `json:"media_type"`
				Data      string `json:"data"`
			} `json:"source"`
		}{"inline", value.Name, value.Description, struct {
			Type      string `json:"type"`
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
		}{"base64", "application/zip", value.Data}})
	default:
		return nil, invalid("", "expected a shell skill")
	}
}

func decodeShellEnvironment(raw json.RawMessage) (generation.Optional[generation.ShellEnvironment], error) {
	var result generation.Optional[generation.ShellEnvironment]
	if len(raw) == 0 {
		return result, nil
	}
	if absent(raw) {
		return generation.Null[generation.ShellEnvironment](), nil
	}
	var value struct {
		Type        string `json:"type"`
		ContainerID string `json:"container_id"`
	}
	if json.Unmarshal(raw, &value) != nil {
		return result, failure(generation.ProtocolError)
	}
	switch value.Type {
	case "local":
		return generation.Some[generation.ShellEnvironment](generation.ShellLocalEnvironment{}), nil
	case "container_reference":
		if requiredString("container_id", value.ContainerID) == nil {
			return generation.Some[generation.ShellEnvironment](generation.ShellContainerReference{ContainerID: value.ContainerID}), nil
		}
	}
	return result, failure(generation.ProtocolError)
}
