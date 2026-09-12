package openai

import "github.com/deyna256/clan/internal/generation"

func (s *streamState) mergeToolSearch(index int, item *streamItem, next generation.Item) ([]generation.Event, error) {
	var err error
	switch value := next.(type) {
	case generation.OpenAIToolSearchCall:
		previous := item.item.(generation.OpenAIToolSearchCall)
		value.CallID, err = mergeCallField(previous.CallID, value.CallID)
		if err != nil || previous.Execution != value.Execution {
			return nil, protocolError()
		}
		value.CreatedBy, err = mergeCallField(previous.CreatedBy, value.CreatedBy)
		next = value
	case generation.OpenAIToolSearchOutput:
		previous := item.item.(generation.OpenAIToolSearchOutput)
		value.CallID, err = mergeCallField(previous.CallID, value.CallID)
		if err != nil || previous.Execution != value.Execution {
			return nil, protocolError()
		}
		value.CreatedBy, err = mergeCallField(previous.CreatedBy, value.CreatedBy)
		next = value
	case generation.OpenAIAdditionalTools:
		if item.item.(generation.OpenAIAdditionalTools).Role != value.Role {
			return nil, protocolError()
		}
	}
	if err != nil {
		return nil, err
	}
	if err := s.grow(metadataBytes(next) - metadataBytes(item.item)); err != nil {
		return nil, err
	}
	item.item = next
	if item.started {
		return nil, nil
	}
	item.started = true
	// Atomic search data is published at ItemEnded, after all snapshots are merged.
	switch value := next.(type) {
	case generation.OpenAIToolSearchCall:
		value.Arguments = nil
		next = value
	case generation.OpenAIToolSearchOutput:
		value.Tools = nil
		next = value
	case generation.OpenAIAdditionalTools:
		value.Tools = nil
		next = value
	}
	return []generation.Event{generation.ItemStarted{Index: index, Item: next}}, nil
}

func toolSearchBytes(item generation.Item) int {
	switch value := item.(type) {
	case generation.OpenAIToolSearchCall:
		return optionalStringBytes(value.ID, value.CallID, value.Execution, value.CreatedBy) + len(value.Arguments)
	case generation.OpenAIToolSearchOutput:
		return optionalStringBytes(value.ID, value.CallID, value.Execution, value.CreatedBy) + loadedToolsBytes(value.Tools)
	case generation.OpenAIAdditionalTools:
		return optionalStringBytes(value.ID) + len(value.Role) + loadedToolsBytes(value.Tools)
	default:
		return 0
	}
}

func optionalStringBytes(values ...generation.Optional[string]) int {
	size := 0
	for _, optional := range values {
		value, _ := optional.Value()
		size += len(value)
	}
	return size
}

func loadedToolsBytes(tools []generation.Tool) int {
	size := 0
	for _, tool := range tools {
		size += 128
		switch value := tool.(type) {
		case generation.FunctionTool:
			callers, _ := value.OpenAI.AllowedCallers.Value()
			size += len(value.Name) + optionalStringBytes(value.Description) + len(value.Parameters) + len(value.OpenAI.OutputSchema) + stringListBytes(callers)
		case generation.CustomTool:
			callers, _ := value.OpenAI.AllowedCallers.Value()
			size += len(value.Name) + optionalStringBytes(value.Description) + stringListBytes(callers)
			if grammar, ok := value.Format.(generation.CustomGrammarFormat); ok {
				size += len(grammar.Syntax) + len(grammar.Definition)
			}
		case generation.OpenAINamespaceTool:
			size += len(value.Name) + len(value.Description) + loadedToolsBytes(value.Tools)
		case generation.OpenAIMCPTool:
			size += loadedMCPBytes(value)
		case generation.OpenAIToolSearchTool:
			size += optionalStringBytes(value.Description, value.Execution) + len(value.Parameters)
		case generation.OpenAIWebSearchTool:
			filters, _ := value.Filters.Value()
			domains, _ := filters.AllowedDomains.Value()
			location, _ := value.Location.Value()
			size += len(value.Version) + len(value.ContextSize) + stringListBytes(domains) + stringListBytes(value.SearchContentTypes) + optionalStringBytes(location.Type, location.City, location.Country, location.Region, location.Timezone)
		case generation.OpenAIFileSearchTool:
			filter, _ := value.Filter.Value()
			ranking, _ := value.Ranking.Value()
			size += stringListBytes(value.VectorStoreIDs) + len(ranking.Ranker) + loadedFilterBytes(filter)
		case generation.OpenAIComputerPreviewTool:
			size += len(value.Environment)
		case generation.OpenAICodeInterpreterTool:
			callers, _ := value.AllowedCallers.Value()
			size += stringListBytes(callers)
			switch container := value.Container.(type) {
			case generation.InterpreterContainerID:
				size += len(container)
			case generation.InterpreterAutoContainer:
				size += stringListBytes(container.FileIDs) + optionalStringBytes(container.MemoryLimit) + loadedNetworkBytes(container.NetworkPolicy)
			}
		case generation.OpenAIImageGenerationTool:
			mask, _ := value.Mask.Value()
			size += optionalStringBytes(value.Model) + len(value.Action) + len(value.Background) + len(value.OutputFormat) + len(value.Quality) + len(value.Size) + len(value.Moderation) + optionalStringBytes(value.InputFidelity, mask.FileID, mask.ImageURL)
		case generation.OpenAIShellTool:
			callers, _ := value.AllowedCallers.Value()
			environment, _ := value.Environment.Value()
			size += stringListBytes(callers) + loadedShellEnvironmentBytes(environment)
		case generation.OpenAIApplyPatchTool:
			callers, _ := value.AllowedCallers.Value()
			size += stringListBytes(callers)

		}
	}
	return size
}

func loadedMCPBytes(tool generation.OpenAIMCPTool) int {
	size := len(tool.ServerLabel) + optionalStringBytes(tool.Authorization, tool.ServerDescription)
	switch endpoint := tool.Endpoint.(type) {
	case generation.MCPServerURL:
		size += len(endpoint)
	case generation.MCPConnectorID:
		size += len(endpoint)
	case generation.MCPTunnelID:
		size += len(endpoint)
	}
	headers, _ := tool.Headers.Value()
	for name, value := range headers {
		size += 64 + len(name) + len(value)
	}
	callers, _ := tool.AllowedCallers.Value()
	size += stringListBytes(callers)
	allowed, _ := tool.AllowedTools.Value()
	switch value := allowed.(type) {
	case generation.MCPToolNames:
		size += stringListBytes(value)
	case generation.MCPToolFilter:
		size += stringListBytes(value.ToolNames)
	}
	approval, _ := tool.RequireApproval.Value()
	if filter, ok := approval.(generation.MCPApprovalFilter); ok {
		always, _ := filter.Always.Value()
		never, _ := filter.Never.Value()
		size += stringListBytes(always.ToolNames) + stringListBytes(never.ToolNames)
	}
	return size
}

func stringListBytes(values []string) int {
	size := 0
	for _, value := range values {
		size += 16 + len(value)
	}
	return size
}

func loadedFilterBytes(filter generation.SearchFilter) int {
	switch value := filter.(type) {
	case generation.SearchComparison:
		return 64 + len(value.Key) + len(value.Operator) + len(value.Value)
	case generation.SearchCompound:
		size := 64 + len(value.Operator)
		for _, child := range value.Filters {
			size += loadedFilterBytes(child)
		}
		return size
	default:
		return 0
	}
}

func loadedNetworkBytes(policy generation.InterpreterNetworkPolicy) int {
	value, ok := policy.(generation.InterpreterNetworkAllowlist)
	if !ok {
		return 0
	}
	size := stringListBytes(value.Domains)
	for _, secret := range value.Secrets {
		size += 64 + len(secret.Domain) + len(secret.Name) + len(secret.Value)
	}
	return size
}

func loadedShellEnvironmentBytes(environment generation.ShellEnvironment) int {
	switch value := environment.(type) {
	case generation.ShellContainerReference:
		return len(value.ContainerID)
	case generation.ShellLocalEnvironment:
		size := 0
		for _, skill := range value.Skills {
			size += 64 + len(skill.Name) + len(skill.Description) + len(skill.Path)
		}
		return size
	case generation.ShellAutoContainer:
		size := stringListBytes(value.FileIDs) + optionalStringBytes(value.MemoryLimit) + loadedNetworkBytes(value.NetworkPolicy)
		for _, skill := range value.Skills {
			size += 64
			switch skill := skill.(type) {
			case generation.ShellSkillReference:
				size += len(skill.SkillID) + optionalStringBytes(skill.Version)
			case generation.ShellInlineSkill:
				size += len(skill.Name) + len(skill.Description) + len(skill.Data)
			}
		}
		return size
	default:
		return 0
	}
}
