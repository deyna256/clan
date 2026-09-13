package management

import (
	"context"
	"crypto/rand"
	"strings"

	"github.com/deyna256/clan/internal/accesskey"
)

type keyView struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Enabled          bool   `json:"enabled"`
	ConcurrencyLimit int    `json:"concurrency_limit"`
}

type createKeyInput struct {
	Body struct {
		Name             string `json:"name" minLength:"1" pattern:"\\S"`
		ConcurrencyLimit int    `json:"concurrency_limit" minimum:"-1"`
	}
}

type createKeyOutput struct {
	Body struct {
		ID               string `json:"id"`
		Name             string `json:"name"`
		Enabled          bool   `json:"enabled"`
		ConcurrencyLimit int    `json:"concurrency_limit"`
		Key              string `json:"key"`
	}
}

type updateKeyInput struct {
	ID   string `path:"id" minLength:"1" pattern:"\\S"`
	Body struct {
		ConcurrencyLimit int `json:"concurrency_limit" minimum:"-1"`
	}
}

type keysInput struct {
	Limit   int    `query:"limit" default:"50" minimum:"1" maximum:"100"`
	Offset  int64  `query:"offset" default:"0" minimum:"0"`
	Q       string `query:"q"`
	Enabled string `query:"enabled" enum:"true,false"`
}

type keysOutput struct {
	Body page[keyView]
}

func (s *service) createKey(ctx context.Context, input *createKeyInput) (*createKeyOutput, error) {
	if strings.TrimSpace(input.Body.Name) == "" {
		return nil, problem(422, "invalid-request", "A nonblank name is required.")
	}
	key, err := accesskey.New(accesskey.Identity{ID: accesskey.ID(rand.Text()), Name: input.Body.Name}, true)
	if err != nil {
		return nil, s.failure(ctx, err, true)
	}
	raw, hash := accesskey.Generate()
	if err := s.store.CreateAccessKey(ctx, key, hash, input.Body.ConcurrencyLimit); err != nil {
		return nil, s.failure(ctx, err, true)
	}
	output := &createKeyOutput{}
	output.Body.ID = string(key.Identity().ID)
	output.Body.Name = key.Identity().Name
	output.Body.Enabled = true
	output.Body.ConcurrencyLimit = input.Body.ConcurrencyLimit
	output.Body.Key = raw
	return output, nil
}

func (s *service) listKeys(ctx context.Context, input *keysInput) (*keysOutput, error) {
	records, err := s.store.ListAccessKeys(ctx)
	if err != nil {
		return nil, s.failure(ctx, err, false)
	}
	items := make([]keyView, 0, len(records))
	for _, record := range records {
		identity := record.Key.Identity()
		if !matches(input.Q, string(identity.ID), identity.Name) {
			continue
		}
		if input.Enabled != "" && record.Key.Enabled() != (input.Enabled == "true") {
			continue
		}
		items = append(items, keyView{
			ID: string(identity.ID), Name: identity.Name, Enabled: record.Key.Enabled(), ConcurrencyLimit: record.ConcurrencyLimit,
		})
	}
	return &keysOutput{Body: paginate(items, listInput{Limit: input.Limit, Offset: input.Offset})}, nil
}

func (s *service) updateKey(ctx context.Context, input *updateKeyInput) (*struct{}, error) {
	err := s.executor.SetConcurrency(ctx, accesskey.ID(input.ID), input.Body.ConcurrencyLimit)
	return nil, s.failure(ctx, err, true)
}

func (s *service) revokeKey(ctx context.Context, input *resourceInput) (*struct{}, error) {
	return nil, s.failure(ctx, s.executor.RevokeKey(ctx, accesskey.ID(input.ID)), true)
}
