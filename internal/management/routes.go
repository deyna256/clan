package management

import (
	"context"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
)

func (s *service) routes(api huma.API) {
	register(api, huma.Operation{OperationID: "summarize-usage", Method: http.MethodGet, Path: "/usage"}, s.summarizeUsage)
	register(api, huma.Operation{OperationID: "list-requests", Method: http.MethodGet, Path: "/requests"}, s.listRequests)
	register(api, huma.Operation{OperationID: "get-request", Method: http.MethodGet, Path: "/requests/{id}"}, s.getRequest)
	register(api, huma.Operation{OperationID: "start-login", Method: http.MethodPost, Path: "/oauth/login"}, s.startLogin)
	register(api, huma.Operation{OperationID: "login-status", Method: http.MethodGet, Path: "/oauth/login"}, s.loginStatus)
	register(api, huma.Operation{
		OperationID: "cancel-login", Method: http.MethodPost, Path: "/oauth/login/cancel", DefaultStatus: 204,
	}, s.cancelLogin)
	register(api, huma.Operation{OperationID: "list-accounts", Method: http.MethodGet, Path: "/accounts"}, s.listAccounts)
	register(api, huma.Operation{OperationID: "get-account", Method: http.MethodGet, Path: "/accounts/{id}"}, s.getAccount)
	register(api, huma.Operation{
		OperationID: "reconnect-account", Method: http.MethodPost, Path: "/accounts/{id}/reconnect",
	}, s.reconnect)
	register(api, huma.Operation{
		OperationID: "disable-account", Method: http.MethodPost, Path: "/accounts/{id}/disable", DefaultStatus: 204,
	}, s.disableAccount)
	register(api, huma.Operation{
		OperationID: "enable-account", Method: http.MethodPost, Path: "/accounts/{id}/enable", DefaultStatus: 204,
	}, s.enableAccount)
	register(api, huma.Operation{
		OperationID: "delete-account", Method: http.MethodDelete, Path: "/accounts/{id}", DefaultStatus: 204,
	}, s.deleteAccount)
	register(api, huma.Operation{
		OperationID: "create-client-key", Method: http.MethodPost, Path: "/client-keys", DefaultStatus: 201,
	}, s.createKey)
	register(api, huma.Operation{OperationID: "list-client-keys", Method: http.MethodGet, Path: "/client-keys"}, s.listKeys)
	register(api, huma.Operation{
		OperationID: "update-client-key", Method: http.MethodPatch, Path: "/client-keys/{id}", DefaultStatus: 204,
	}, s.updateKey)
	register(api, huma.Operation{
		OperationID: "revoke-client-key", Method: http.MethodPost, Path: "/client-keys/{id}/revoke", DefaultStatus: 204,
	}, s.revokeKey)
	register(api, huma.Operation{OperationID: "list-models", Method: http.MethodGet, Path: "/models"}, s.listModels)
}

type resourceInput struct {
	ID string `path:"id" minLength:"1" pattern:"\\S"`
}

// ListInput defines pagination and filtering parameters; it must be exported
// so Huma includes its fields when embedded in other request types.
type ListInput struct {
	Limit  int    `query:"limit" default:"50" minimum:"1" maximum:"100"`
	Offset int64  `query:"offset" default:"0" minimum:"0"`
	Q      string `query:"q"`
}

type page[T any] struct {
	Items  []T   `json:"items"`
	Total  int   `json:"total"`
	Limit  int   `json:"limit"`
	Offset int64 `json:"offset"`
}

func paginate[T any](items []T, input ListInput) page[T] {
	start := int(min(input.Offset, int64(len(items))))
	end := start + min(input.Limit, len(items)-start)
	return page[T]{Items: items[start:end], Total: len(items), Limit: input.Limit, Offset: input.Offset}
}

func matches(query, id, name string) bool {
	query = strings.ToLower(query)
	return strings.Contains(strings.ToLower(id), query) || strings.Contains(strings.ToLower(name), query)
}

type modelView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type modelsOutput struct {
	Body page[modelView]
}

func (s *service) listModels(ctx context.Context, input *ListInput) (*modelsOutput, error) {
	models, err := s.executor.AvailableModels(ctx)
	if err != nil {
		return nil, s.failure(ctx, err, false)
	}
	items := make([]modelView, 0, len(models))
	for _, model := range models {
		if matches(input.Q, model.ID, model.Name) {
			items = append(items, modelView{ID: model.ID, Name: model.Name})
		}
	}
	return &modelsOutput{Body: paginate(items, *input)}, nil
}
