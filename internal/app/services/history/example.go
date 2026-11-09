package history

import (
	"context"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	"github.com/sorrelfox/postsonde/internal/core/history"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
)

// Secrets returns the secret values PostSonde keeps: the open project's
// and the app's (as logexport.Secrets). Values may come with an error for
// the ones that could not be read; they are used, and the error is
// logged.
type Secrets func(ctx context.Context) ([]string, error)

// WithSecrets masks the response examples Example makes with the values
// secrets returns, besides the masking the entry got when it was
// recorded. Without it only that masking applies, and the credentials of
// Authorization fields by name.
func WithSecrets(secrets Secrets) Option {
	return func(s *HistoryService) { s.secrets = secrets }
}

// ExampleInputDTO is what Example takes.
type ExampleInputDTO struct {
	// ID is the history entry.
	ID string `json:"id"`
	// Name is the example's name; blank names it by its status
	// ("201 Created").
	Name string `json:"name"`
}

// Example returns history entry in.ID as a response example (HTTP-38):
// its response and the request as sent, masked again with every secret
// PostSonde keeps (WithSecrets), bodies of at most 64 KiB as the entry
// keeps them (docs/history.md#response-examples). Nothing is written: the
// UI adds the example to the request's draft, which saves it into the
// request file.
//
// Errors: history.not_found, history.no_response for a send without a
// response, workspace.none_open.
func (s *HistoryService) Example(ctx context.Context, in ExampleInputDTO) (collection.ResponseExampleDTO, error) {
	st, _, err := s.current(ctx)
	if err != nil {
		return collection.ResponseExampleDTO{}, err
	}
	e, err := st.Get(in.ID)
	if err != nil {
		return collection.ResponseExampleDTO{}, err
	}
	var values []string
	if s.secrets != nil {
		v, err := s.secrets(ctx)
		if err != nil {
			s.logger.Warn("some secrets could not be read to mask a response example", "err", err)
		}
		values = v
	}
	ex, masked, err := history.Example(e, in.Name, time.Now(), httpclient.NewSecretMasker(values))
	if err != nil {
		return collection.ResponseExampleDTO{}, err
	}
	s.logger.Debug("response example made", "id", in.ID, "masked", masked)
	return collection.ToResponseExampleDTO(ex), nil
}
