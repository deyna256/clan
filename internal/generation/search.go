package generation

import "encoding/json"

type OpenAIWebSearchTool struct {
	Version            string // Empty selects web_search.
	ExternalWebAccess  Optional[bool]
	Filters            Optional[WebSearchFilters]
	SearchContentTypes []string
	ContextSize        string
	Location           Optional[SearchLocation]
}
type WebSearchFilters struct{ AllowedDomains Optional[[]string] }

type SearchLocation struct{ Type, City, Country, Region, Timezone Optional[string] }

type OpenAIFileSearchTool struct {
	VectorStoreIDs []string
	MaxResults     Optional[int64]
	Filter         Optional[SearchFilter]
	Ranking        Optional[SearchRanking]
}
type SearchRanking struct {
	Ranker         string
	ScoreThreshold Optional[float64]
	Hybrid         Optional[HybridSearch]
}
type HybridSearch struct{ EmbeddingWeight, TextWeight float64 }

type SearchFilter interface{ isSearchFilter() }
type SearchComparison struct {
	Key, Operator string
	Value         json.RawMessage // Scalar, or a string/number array for in/nin.
}
type SearchCompound struct {
	Operator string
	Filters  []SearchFilter
}

func (SearchComparison) isSearchFilter() {}
func (SearchCompound) isSearchFilter()   {}
func (OpenAIWebSearchTool) isTool()      {}
func (OpenAIFileSearchTool) isTool()     {}

// OpenAISearchChoice selects a hosted search tool rather than a client function.
type OpenAISearchChoice string

func (OpenAISearchChoice) isToolChoice() {}

type OpenAIWebSearchCall struct {
	ID, Status string
	Action     WebSearchAction
}
type WebSearchAction interface{ isWebSearchAction() }
type WebSearch struct {
	Query            Optional[string]
	Queries, Sources []string
}
type WebOpenPage struct{ URL Optional[string] }
type WebFind struct{ URL, Pattern string }

func (WebSearch) isWebSearchAction()   {}
func (WebOpenPage) isWebSearchAction() {}
func (WebFind) isWebSearchAction()     {}
func (OpenAIWebSearchCall) isItem()    {}

type OpenAIFileSearchCall struct {
	ID, Status string
	Queries    []string
	Results    Optional[[]FileSearchResult]
}
type FileSearchResult struct {
	FileID, Filename, Text Optional[string]
	Score                  Optional[float64]
	Attributes             Optional[map[string]json.RawMessage]
}

func (OpenAIFileSearchCall) isItem() {}
