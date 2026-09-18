package codex

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/deyna256/clan/internal/account"
)

const catalogTTL = 5 * time.Minute
const catalogTimeout = 5 * time.Second
const catalogInitialRetry = 1 * time.Second
const catalogMaxRetry = catalogTTL

func retryGap(failures int) time.Duration {
	if failures <= 1 {
		return catalogInitialRetry
	}
	if failures > 9 {
		return catalogMaxRetry
	}
	gap := catalogInitialRetry << (failures - 1)
	if gap > catalogMaxRetry {
		return catalogMaxRetry
	}
	return gap
}

// Model contains account-specific catalog facts used for request validation and listing.
// Listed controls visibility only: hidden models may still be requested explicitly.
type Model struct {
	ID                          string
	Name                        string
	Listed                      bool
	ReasoningEfforts            []string
	InputModalities             []string
	SupportsReasoningSummary    bool
	SupportsVerbosity           bool
	SupportsImageDetailOriginal bool
}

// FetchModels fetches one authenticated catalog with a five-second timeout.
// It does not filter supported_in_api: these are ChatGPT OAuth accounts.
func (c *Client) FetchModels(ctx context.Context, a account.Account) ([]Model, error) {
	ctx, cancel := context.WithTimeout(ctx, catalogTimeout)
	defer cancel()
	response, err := c.do(ctx, a, http.MethodGet, "/models?client_version="+url.QueryEscape(c.version), nil)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	invalidResponse := httpFailure(response)
	data, err := readPayload(response.Body)
	if err != nil {
		category := TransportFailure
		if errors.Is(err, errPayloadTooLarge) {
			category = InvalidResponse
		}
		failure := safeFailure(ctx, category, response.StatusCode, err)
		failure.RetryAfter = invalidResponse.RetryAfter
		return nil, failure
	}
	var wire struct {
		Models []struct {
			ID         string `json:"slug"`
			Name       string `json:"display_name"`
			Visibility string `json:"visibility"`
			Reasoning  []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
			Summary       *bool    `json:"supports_reasoning_summary_parameter"`
			Verbosity     bool     `json:"support_verbosity"`
			OriginalImage bool     `json:"supports_image_detail_original"`
			Modalities    []string `json:"input_modalities"`
		} `json:"models"`
	}
	if json.Unmarshal(data, &wire) != nil || wire.Models == nil {
		return nil, invalidResponse
	}
	models := make([]Model, 0, len(wire.Models))
	seen := make(map[string]bool)
	for _, item := range wire.Models {
		if strings.TrimSpace(item.ID) == "" || seen[item.ID] || !slices.Contains([]string{"list", "hide", "none"}, item.Visibility) {
			return nil, invalidResponse
		}
		seen[item.ID] = true
		model := Model{ID: item.ID, Name: item.Name, Listed: item.Visibility == "list",
			SupportsReasoningSummary: item.Summary == nil || *item.Summary,
			SupportsVerbosity:        item.Verbosity, SupportsImageDetailOriginal: item.OriginalImage}
		model.InputModalities = item.Modalities
		if model.InputModalities == nil {
			model.InputModalities = []string{"text", "image"}
		}
		for _, modality := range model.InputModalities {
			if !slices.Contains([]string{"text", "image", "audio"}, modality) {
				return nil, invalidResponse
			}
		}
		for _, effort := range item.Reasoning {
			if strings.TrimSpace(effort.Effort) == "" {
				return nil, invalidResponse
			}
			model.ReasoningEfforts = append(model.ReasoningEfforts, effort.Effort)
		}
		models = append(models, model)
	}
	return models, nil
}

// AccountCatalog contains models and discovery failures for one enabled account.
// ChatGPTAccountID ties capabilities to the identity used for discovery.
// Loaded distinguishes a successfully loaded empty catalog from an initial failure.
type AccountCatalog struct {
	AccountID        account.ID
	ChatGPTAccountID string
	Models           []Model
	Loaded           bool
	Failure          *Failure
}

// CatalogSnapshot owns its data and includes enabled accounts only.
// Accounts holds account-specific capability facts; Listed is the deduplicated visible union.
type CatalogSnapshot struct {
	Accounts []AccountCatalog
	Listed   []Model
}

// ErrCatalogUnavailable means discovery failures left no successfully loaded enabled account.
var ErrCatalogUnavailable = errors.New("codex: model discovery unavailable")

// ErrCatalogClosed means the catalog no longer accepts accounts or snapshots.
var ErrCatalogClosed = errors.New("codex: catalog is closed")

// Catalog refreshes enabled accounts on demand. Construct it with NewCatalog.
// SetAccounts is the sole authority for membership; snapshots cannot reintroduce deleted accounts.
// Each shared refresh runs for at most five seconds and survives individual waiter cancellation.
// The owner must call Close to stop and wait for refreshes during shutdown.
type Catalog struct {
	fetch    func(context.Context, account.Account) ([]Model, error)
	mu       sync.Mutex
	wg       sync.WaitGroup
	closed   bool
	accounts map[account.ID]*catalogEntry
}

type catalogEntry struct {
	account     account.Account
	models      []Model
	loaded      bool
	failure     *Failure
	failures    int
	nextRefresh time.Time
	refresh     *catalogRefresh
}

type catalogRefresh struct {
	done   chan struct{}
	cancel context.CancelFunc
}

// NewCatalog creates an initially empty catalog. SetAccounts supplies enabled accounts.
func NewCatalog(fetch func(context.Context, account.Account) ([]Model, error)) (*Catalog, error) {
	if fetch == nil {
		return nil, errors.New("codex: model fetch function is required")
	}
	return &Catalog{fetch: fetch, accounts: make(map[account.ID]*catalogEntry)}, nil
}

// SetAccounts replaces enabled membership atomically. Pass only enabled accounts,
// after each management change; nil disables all and cancels pending refreshes.
// Credential rotation preserves last-good data for the same ChatGPT identity.
func (c *Catalog) SetAccounts(accounts []account.Account) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ErrCatalogClosed
	}
	active := make(map[account.ID]account.Account, len(accounts))
	for _, a := range accounts {
		id := a.Identity().ID
		if id == "" || account.ValidateCredentials(a.Credentials()) != nil {
			return errors.New("codex: invalid catalog account")
		}
		if _, duplicate := active[id]; duplicate {
			return errors.New("codex: duplicate catalog account")
		}
		active[id] = a
	}
	for id, entry := range c.accounts {
		a, exists := active[id]
		if !exists {
			if entry.refresh != nil {
				entry.refresh.cancel()
			}
			delete(c.accounts, id)
			continue
		}
		if entry.account.Credentials() != a.Credentials() {
			if entry.refresh != nil {
				entry.refresh.cancel()
				entry.refresh = nil
			}
			if entry.account.Credentials().ChatGPTAccountID != a.Credentials().ChatGPTAccountID {
				entry.models = nil
				entry.loaded = false
			}
			entry.failure = nil
			entry.failures = 0
			entry.nextRefresh = time.Time{}
		}
		entry.account = a
	}
	for id, a := range active {
		if _, exists := c.accounts[id]; !exists {
			c.accounts[id] = &catalogEntry{account: a}
		}
	}
	return nil
}

// Snapshot shares due refreshes, then returns the enabled union and per-account failures.
// A caller's cancellation stops its wait only. Transient refresh failure retains last-good data.
func (c *Catalog) Snapshot(ctx context.Context) (CatalogSnapshot, error) {
	for {
		if err := ctx.Err(); err != nil {
			return CatalogSnapshot{}, err
		}
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return CatalogSnapshot{}, ErrCatalogClosed
		}
		waits := make([]<-chan struct{}, 0, len(c.accounts))
		for _, entry := range c.accounts {
			if entry.refresh == nil && !time.Now().Before(entry.nextRefresh) {
				refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), catalogTimeout)
				job := &catalogRefresh{done: make(chan struct{}), cancel: cancel}
				entry.refresh = job
				entry.nextRefresh = time.Now().Add(catalogTTL)
				a := entry.account
				c.wg.Go(func() { c.refresh(refreshCtx, entry, a, job) })
			}
			if entry.refresh != nil {
				waits = append(waits, entry.refresh.done)
			}
		}
		if len(waits) == 0 {
			break
		}
		c.mu.Unlock()
		for _, done := range waits {
			select {
			case <-ctx.Done():
				return CatalogSnapshot{}, ctx.Err()
			case <-done:
			}
		}
	}
	defer c.mu.Unlock()
	result := CatalogSnapshot{Accounts: make([]AccountCatalog, 0, len(c.accounts)), Listed: []Model{}}
	ids := slices.Sorted(maps.Keys(c.accounts))
	listed := make(map[string]bool)
	loaded := false
	failed := false
	for _, id := range ids {
		entry := c.accounts[id]
		catalog := AccountCatalog{AccountID: id, ChatGPTAccountID: entry.account.Credentials().ChatGPTAccountID,
			Models: cloneModels(entry.models), Loaded: entry.loaded}
		if entry.failure != nil {
			failure := *entry.failure
			catalog.Failure = &failure
			failed = true
		}
		result.Accounts = append(result.Accounts, catalog)
		loaded = loaded || entry.loaded
		for _, model := range cloneModels(entry.models) {
			if model.Listed && !listed[model.ID] {
				result.Listed = append(result.Listed, model)
				listed[model.ID] = true
			}
		}
	}
	sort.Slice(result.Listed, func(i, j int) bool { return result.Listed[i].ID < result.Listed[j].ID })
	if failed && !loaded {
		return result, ErrCatalogUnavailable
	}
	return result, nil
}

// Close prevents new work, cancels refreshes and waits for all of their cleanup.
// It is safe to call repeatedly and concurrently; a closed catalog cannot reopen.
func (c *Catalog) Close() {
	c.mu.Lock()
	c.closed = true
	for _, entry := range c.accounts {
		if entry.refresh != nil {
			entry.refresh.cancel()
		}
	}
	c.mu.Unlock()
	c.wg.Wait()
}

func (c *Catalog) refresh(ctx context.Context, entry *catalogEntry, a account.Account, job *catalogRefresh) {
	defer close(job.done)
	defer job.cancel()
	models, err := c.fetch(ctx, a)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.accounts[a.Identity().ID] != entry || entry.refresh != job {
		return
	}
	entry.refresh = nil
	entry.failure = nil
	if err == nil {
		entry.models = models
		entry.loaded = true
		entry.failures = 0
		entry.nextRefresh = time.Now().Add(catalogTTL)
		return
	}
	var failure *Failure
	if !errors.As(err, &failure) {
		failure = &Failure{Category: TransportFailure}
	}
	entry.failure = failure
	transient := failure.Category == TransportFailure || failure.Category == ProviderLimit || failure.HTTPStatus >= 500
	if !transient {
		entry.models = nil
		entry.loaded = false
	}
	if entry.loaded {
		entry.nextRefresh = time.Now().Add(catalogTTL)
	} else {
		entry.failures++
		entry.nextRefresh = time.Now().Add(retryGap(entry.failures))
	}
}

func cloneModels(models []Model) []Model {
	cloned := slices.Clone(models)
	for i := range cloned {
		cloned[i].ReasoningEfforts = slices.Clone(cloned[i].ReasoningEfforts)
		cloned[i].InputModalities = slices.Clone(cloned[i].InputModalities)
	}
	return cloned
}
