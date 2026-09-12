package generation

type OpenAIImageGenerationTool struct {
	Model                                           Optional[string]
	Action, Background, OutputFormat, Quality, Size string
	InputFidelity                                   Optional[string]
	Moderation                                      string
	OutputCompression, PartialImages                Optional[int64]
	Mask                                            Optional[ImageGenerationMask]
}
type ImageGenerationMask struct{ FileID, ImageURL Optional[string] }

type OpenAIImageGenerationChoice struct{}

func (OpenAIImageGenerationTool) isTool()         {}
func (OpenAIImageGenerationChoice) isToolChoice() {}

type OpenAIImageGenerationCall struct {
	ID, Status string
	Result     Optional[string] // Base64 image data; never a preview.
	Metadata   ImageGenerationMetadata
}
type ImageGenerationMetadata struct {
	Action, Background, OutputFormat, Quality, Size, RevisedPrompt Optional[string]
}

func (OpenAIImageGenerationCall) isItem() {}

// ImagePreview carries a complete preview, not bytes to append to the result.
type ImagePreview struct {
	ItemIndex, Index int
	Base64           string
	Metadata         ImageGenerationMetadata
}

func (ImagePreview) isEvent() {}
