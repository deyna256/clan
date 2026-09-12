package wire

import (
	"encoding/json"

	"github.com/deyna256/clan/internal/generation"
)

func decodeLoadedShell(raw json.RawMessage) (generation.Tool, error) {
	var value struct {
		AllowedCallers generation.Optional[[]string] `json:"allowed_callers"`
		Environment    json.RawMessage               `json:"environment"`
	}
	if json.Unmarshal(raw, &value) != nil {
		return nil, failure(generation.ProtocolError)
	}
	environment, err := decodeLoadedShellEnvironment(value.Environment)
	if err != nil {
		return nil, err
	}
	return generation.OpenAIShellTool{AllowedCallers: value.AllowedCallers, Environment: environment}, nil
}

func decodeLoadedShellEnvironment(raw json.RawMessage) (generation.Optional[generation.ShellEnvironment], error) {
	var result generation.Optional[generation.ShellEnvironment]
	if len(raw) == 0 {
		return result, nil
	}
	if absent(raw) {
		return generation.Null[generation.ShellEnvironment](), nil
	}
	var value struct {
		Type          string                                 `json:"type"`
		ContainerID   string                                 `json:"container_id"`
		FileIDs       strictStrings                          `json:"file_ids"`
		MemoryLimit   generation.Optional[string]            `json:"memory_limit"`
		NetworkPolicy json.RawMessage                        `json:"network_policy"`
		Skills        generation.Optional[[]json.RawMessage] `json:"skills"`
	}
	if json.Unmarshal(raw, &value) != nil || value.Skills.IsNull() {
		return result, failure(generation.ProtocolError)
	}
	skills, hasSkills := value.Skills.Value()
	switch value.Type {
	case "container_reference":
		return generation.Some[generation.ShellEnvironment](generation.ShellContainerReference{ContainerID: value.ContainerID}), nil
	case "local":
		var local []generation.ShellLocalSkill
		if hasSkills {
			local = make([]generation.ShellLocalSkill, 0, len(skills))
		}
		for _, raw := range skills {
			var skill struct {
				Name        string  `json:"name"`
				Description *string `json:"description"`
				Path        string  `json:"path"`
			}
			if json.Unmarshal(raw, &skill) != nil || skill.Description == nil {
				return result, failure(generation.ProtocolError)
			}
			local = append(local, generation.ShellLocalSkill{Name: skill.Name, Description: *skill.Description, Path: skill.Path})
		}
		return generation.Some[generation.ShellEnvironment](generation.ShellLocalEnvironment{Skills: local}), nil
	case "container_auto":
		network, err := decodeLoadedNetwork(value.NetworkPolicy)
		if err != nil {
			return result, err
		}
		var decoded []generation.ShellSkill
		if hasSkills {
			decoded = make([]generation.ShellSkill, 0, len(skills))
		}
		for _, raw := range skills {
			skill, err := decodeLoadedShellSkill(raw)
			if err != nil {
				return result, err
			}
			decoded = append(decoded, skill)
		}
		return generation.Some[generation.ShellEnvironment](generation.ShellAutoContainer{FileIDs: []string(value.FileIDs), MemoryLimit: value.MemoryLimit, NetworkPolicy: network, Skills: decoded}), nil
	default:
		return result, failure(generation.ProtocolError)
	}
}

func decodeLoadedShellSkill(raw json.RawMessage) (generation.ShellSkill, error) {
	var value struct {
		Type        string                      `json:"type"`
		SkillID     string                      `json:"skill_id"`
		Version     generation.Optional[string] `json:"version"`
		Name        string                      `json:"name"`
		Description *string                     `json:"description"`
		Source      struct {
			Type      string `json:"type"`
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
		} `json:"source"`
	}
	if json.Unmarshal(raw, &value) != nil {
		return nil, failure(generation.ProtocolError)
	}
	switch value.Type {
	case "skill_reference":
		return generation.ShellSkillReference{SkillID: value.SkillID, Version: value.Version}, nil
	case "inline":
		if value.Description == nil || value.Source.Type != "base64" || value.Source.MediaType != "application/zip" {
			return nil, failure(generation.ProtocolError)
		}
		return generation.ShellInlineSkill{Name: value.Name, Description: *value.Description, Data: value.Source.Data}, nil
	default:
		return nil, failure(generation.ProtocolError)
	}
}

func decodeLoadedNetwork(raw json.RawMessage) (generation.InterpreterNetworkPolicy, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var value struct {
		Type           string        `json:"type"`
		AllowedDomains strictStrings `json:"allowed_domains"`
		DomainSecrets  generation.Optional[[]struct {
			Domain string  `json:"domain"`
			Name   string  `json:"name"`
			Value  *string `json:"value"`
		}] `json:"domain_secrets"`
	}
	if json.Unmarshal(raw, &value) != nil || value.DomainSecrets.IsNull() {
		return nil, failure(generation.ProtocolError)
	}
	switch value.Type {
	case "disabled":
		return generation.InterpreterNetworkDisabled{}, nil
	case "allowlist":
		if value.AllowedDomains == nil {
			return nil, failure(generation.ProtocolError)
		}
		var secrets []generation.InterpreterDomainSecret
		if rawSecrets, ok := value.DomainSecrets.Value(); ok {
			secrets = make([]generation.InterpreterDomainSecret, len(rawSecrets))
			for i, secret := range rawSecrets {
				if secret.Value == nil {
					return nil, failure(generation.ProtocolError)
				}
				secrets[i] = generation.InterpreterDomainSecret{Domain: secret.Domain, Name: secret.Name, Value: *secret.Value}
			}
		}
		return generation.InterpreterNetworkAllowlist{Domains: []string(value.AllowedDomains), Secrets: secrets}, nil
	default:
		return nil, failure(generation.ProtocolError)
	}
}
