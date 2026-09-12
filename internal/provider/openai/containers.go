package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"unicode/utf8"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai/internal/wire"
)

type ContainerCreate struct {
	Name          string
	ExpiresAfter  generation.Optional[ContainerExpiration]
	FileIDs       []string
	MemoryLimit   generation.Optional[string]
	NetworkPolicy generation.InterpreterNetworkPolicy
	Skills        []generation.ShellSkill
}

type ContainerExpiration struct {
	Anchor  string `json:"anchor"`
	Minutes int64  `json:"minutes"`
}

// ContainerExpiry permits omitted fields in the returned expiration object.
type ContainerExpiry struct {
	Anchor  generation.Optional[string] `json:"anchor,omitzero"`
	Minutes generation.Optional[int64]  `json:"minutes,omitzero"`
}

type ContainerNetwork struct {
	Type           string                        `json:"type"`
	AllowedDomains generation.Optional[[]string] `json:"allowed_domains,omitzero"`
}

type Container struct {
	ID            string                                `json:"id"`
	CreatedAt     int64                                 `json:"created_at"`
	Name          string                                `json:"name"`
	Object        string                                `json:"object"`
	Status        string                                `json:"status"`
	ExpiresAfter  generation.Optional[ContainerExpiry]  `json:"expires_after,omitzero"`
	LastActiveAt  generation.Optional[int64]            `json:"last_active_at,omitzero"`
	MemoryLimit   generation.Optional[string]           `json:"memory_limit,omitzero"`
	NetworkPolicy generation.Optional[ContainerNetwork] `json:"network_policy,omitzero"`
}

type ContainerFile struct {
	ID          string `json:"id"`
	Bytes       int64  `json:"bytes"`
	ContainerID string `json:"container_id"`
	CreatedAt   int64  `json:"created_at"`
	Object      string `json:"object"`
	Path        string `json:"path"`
	Source      string `json:"source"`
}

type ContainerFileListParams struct {
	After generation.Optional[string]
	Limit generation.Optional[int64]
	Order generation.Optional[string]
}

type ContainerFilePage struct {
	Data    []ContainerFile `json:"data"`
	FirstID string          `json:"first_id"`
	LastID  string          `json:"last_id"`
	HasMore bool            `json:"has_more"`
	Object  string          `json:"object"`
}

func (c *Client) CreateContainer(ctx context.Context, attempt Attempt, request ContainerCreate) (Container, error) {
	payload, err := encodeContainer(request)
	if err != nil {
		return Container{}, err
	}
	return decodeContainer(c.resourceJSON(ctx, attempt, http.MethodPost, []string{"containers"}, nil, payload))
}

func (c *Client) RetrieveContainer(ctx context.Context, attempt Attempt, containerID string) (Container, error) {
	container, err := decodeContainer(c.resourceJSON(ctx, attempt, http.MethodGet, []string{"containers", containerID}, nil, nil))
	if err == nil && container.ID != containerID {
		return Container{}, protocolError()
	}
	return container, err
}

func (c *Client) DeleteContainer(ctx context.Context, attempt Attempt, containerID string) error {
	_, err := c.resourceJSON(ctx, attempt, http.MethodDelete, []string{"containers", containerID}, nil, nil)
	return err
}

// UploadContainerFile takes ownership of content, including on validation failure.
// Its Close must be safe concurrently with Read and unblock it.
func (c *Client) UploadContainerFile(ctx context.Context, attempt Attempt, containerID, filename string, content io.ReadCloser) (file ContainerFile, err error) {
	source := &uploadSource{ReadCloser: content}
	defer source.finish(&err)
	body, err := c.resourceUpload(ctx, attempt, []string{"containers", containerID, "files"}, nil, filename, source)
	return decodeContainerFile(body, err, containerID)
}

func (c *Client) AttachContainerFile(ctx context.Context, attempt Attempt, containerID, fileID string) (ContainerFile, error) {
	if fileID == "" || !utf8.ValidString(fileID) {
		return ContainerFile{}, &InputError{Field: "file_id", Problem: "a file ID is required"}
	}
	body, err := c.resourceJSON(ctx, attempt, http.MethodPost, []string{"containers", containerID, "files"}, nil, struct {
		FileID string `json:"file_id"`
	}{fileID})
	return decodeContainerFile(body, err, containerID)
}

func (c *Client) ListContainerFiles(ctx context.Context, attempt Attempt, containerID string, params ContainerFileListParams) (ContainerFilePage, error) {
	query, err := containerFileQuery(params)
	if err != nil {
		return ContainerFilePage{}, err
	}
	body, err := c.resourceJSON(ctx, attempt, http.MethodGet, []string{"containers", containerID, "files"}, query, nil)
	page, err := decodeResource[struct {
		Data    []json.RawMessage `json:"data"`
		FirstID string            `json:"first_id"`
		LastID  string            `json:"last_id"`
		HasMore bool              `json:"has_more"`
		Object  string            `json:"object"`
	}](body, err)
	if err != nil {
		return ContainerFilePage{}, err
	}
	if resourceRequired(body, "data", "first_id", "last_id", "has_more", "object") != nil || page.Object != "list" {
		return ContainerFilePage{}, protocolError()
	}
	result := ContainerFilePage{Data: make([]ContainerFile, 0, len(page.Data)), FirstID: page.FirstID, LastID: page.LastID, HasMore: page.HasMore, Object: page.Object}
	for _, raw := range page.Data {
		file, err := decodeContainerFile(raw, nil, containerID)
		if err != nil {
			return ContainerFilePage{}, err
		}
		result.Data = append(result.Data, file)
	}
	return result, nil
}

func (c *Client) RetrieveContainerFile(ctx context.Context, attempt Attempt, containerID, fileID string) (ContainerFile, error) {
	body, err := c.resourceJSON(ctx, attempt, http.MethodGet, []string{"containers", containerID, "files", fileID}, nil, nil)
	file, err := decodeContainerFile(body, err, containerID)
	if err == nil && file.ID != fileID {
		return ContainerFile{}, protocolError()
	}
	return file, err
}

func (c *Client) DeleteContainerFile(ctx context.Context, attempt Attempt, containerID, fileID string) error {
	_, err := c.resourceJSON(ctx, attempt, http.MethodDelete, []string{"containers", containerID, "files", fileID}, nil, nil)
	return err
}

// ContainerFileContent returns a streaming body. The caller must close it.
func (c *Client) ContainerFileContent(ctx context.Context, attempt Attempt, containerID, fileID string) (io.ReadCloser, error) {
	return c.resourceContent(ctx, attempt, []string{"containers", containerID, "files", fileID, "content"})
}

func encodeContainer(request ContainerCreate) (any, error) {
	if !utf8.ValidString(request.Name) {
		return nil, &InputError{Field: "name", Problem: "must be valid UTF-8"}
	}
	if request.ExpiresAfter.IsNull() {
		return nil, &InputError{Field: "expires_after", Problem: "must not be null"}
	}
	if expiry, ok := request.ExpiresAfter.Value(); ok && expiry.Anchor != "last_active_at" {
		return nil, &InputError{Field: "expires_after.anchor", Problem: "expected last_active_at"}
	}
	if !containerMemory(request.MemoryLimit) {
		return nil, &InputError{Field: "memory_limit", Problem: "unsupported memory limit"}
	}
	for _, id := range request.FileIDs {
		if id == "" || !utf8.ValidString(id) {
			return nil, &InputError{Field: "file_ids", Problem: "invalid file ID"}
		}
	}
	network, err := wire.EncodeContainerNetwork(request.NetworkPolicy)
	if err != nil {
		return nil, err
	}
	var skills []json.RawMessage
	if request.Skills != nil {
		skills = make([]json.RawMessage, 0, len(request.Skills))
	}
	for _, skill := range request.Skills {
		encoded, err := wire.EncodeShellSkill(skill)
		if err != nil {
			return nil, err
		}
		skills = append(skills, encoded)
	}
	return struct {
		Name          string                                   `json:"name"`
		ExpiresAfter  generation.Optional[ContainerExpiration] `json:"expires_after,omitzero"`
		FileIDs       []string                                 `json:"file_ids,omitzero"`
		MemoryLimit   generation.Optional[string]              `json:"memory_limit,omitzero"`
		NetworkPolicy json.RawMessage                          `json:"network_policy,omitempty"`
		Skills        []json.RawMessage                        `json:"skills,omitzero"`
	}{request.Name, request.ExpiresAfter, request.FileIDs, request.MemoryLimit, network, skills}, nil
}

func containerMemory(memory generation.Optional[string]) bool {
	if memory.IsNull() {
		return false
	}
	value, ok := memory.Value()
	return !ok || slices.Contains([]string{"1g", "4g", "16g", "64g"}, value)
}

func containerFileQuery(params ContainerFileListParams) (url.Values, error) {
	query := make(url.Values)
	if params.After.IsNull() || params.Limit.IsNull() || params.Order.IsNull() {
		return nil, &InputError{Field: "query", Problem: "pagination fields must not be null"}
	}
	if after, ok := params.After.Value(); ok {
		if !utf8.ValidString(after) {
			return nil, &InputError{Field: "after", Problem: "must be valid UTF-8"}
		}
		query.Set("after", after)
	}
	if limit, ok := params.Limit.Value(); ok {
		if limit < 1 || limit > 100 {
			return nil, &InputError{Field: "limit", Problem: "expected 1 to 100"}
		}
		query.Set("limit", strconv.FormatInt(limit, 10))
	}
	if order, ok := params.Order.Value(); ok {
		if order != "asc" && order != "desc" {
			return nil, &InputError{Field: "order", Problem: "expected asc or desc"}
		}
		query.Set("order", order)
	}
	return query, nil
}

func decodeContainer(body []byte, requestErr error) (Container, error) {
	container, err := decodeResource[Container](body, requestErr)
	if err != nil {
		return Container{}, err
	}
	if resourceRequired(body, "id", "created_at", "name", "object", "status") != nil || container.ID == "" || container.CreatedAt < 0 || container.Object != "container" || container.ExpiresAfter.IsNull() || container.LastActiveAt.IsNull() || container.NetworkPolicy.IsNull() || !containerMemory(container.MemoryLimit) {
		return Container{}, protocolError()
	}
	if active, ok := container.LastActiveAt.Value(); ok && active < 0 {
		return Container{}, protocolError()
	}
	if expiry, ok := container.ExpiresAfter.Value(); ok {
		anchor, exists := expiry.Anchor.Value()
		if expiry.Anchor.IsNull() || expiry.Minutes.IsNull() || (exists && anchor != "last_active_at") {
			return Container{}, protocolError()
		}
	}
	if network, ok := container.NetworkPolicy.Value(); ok {
		if (network.Type != "disabled" && network.Type != "allowlist") || network.AllowedDomains.IsNull() {
			return Container{}, protocolError()
		}
		domains, _ := network.AllowedDomains.Value()
		for _, domain := range domains {
			if domain == "" {
				return Container{}, protocolError()
			}
		}
	}
	return container, nil
}

func decodeContainerFile(body []byte, requestErr error, containerID string) (ContainerFile, error) {
	file, err := decodeResource[ContainerFile](body, requestErr)
	if err != nil {
		return ContainerFile{}, err
	}
	if resourceRequired(body, "id", "bytes", "container_id", "created_at", "object", "path", "source") != nil || file.ID == "" || file.ContainerID != containerID || file.Object != "container.file" || file.Bytes < 0 || file.CreatedAt < 0 {
		return ContainerFile{}, protocolError()
	}
	return file, nil
}
