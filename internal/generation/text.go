package generation

type OpenAITextData struct {
	PromptCacheBreakpoint bool
	Annotations           []Annotation
	Logprobs              []TokenLogprob
}

// Annotation keeps its provider array index for merging stream updates.
type Annotation struct {
	Index    int
	Citation Citation
}

// Citation variants preserve provider character offsets without reindexing text.
type Citation interface{ isCitation() }
type URLCitation struct {
	URL, Title string
	Start, End int64
}
type FileCitation struct {
	FileID, Filename string
	Index            int64
}
type ContainerFileCitation struct {
	ContainerID, FileID, Filename string
	Start, End                    int64
}
type FilePath struct {
	FileID string
	Index  int64
}

func (URLCitation) isCitation()           {}
func (FileCitation) isCitation()          {}
func (ContainerFileCitation) isCitation() {}
func (FilePath) isCitation()              {}

type TokenLogprob struct {
	TokenProbability
	Top []TokenProbability
}
type TokenProbability struct {
	Token   string
	Bytes   []byte
	Logprob float64
}
