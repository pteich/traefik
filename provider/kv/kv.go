package kv

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/kvtools/valkeyrie"
	"github.com/kvtools/valkeyrie/store"

	"github.com/pteich/traefik/job"
	"github.com/pteich/traefik/log"
	"github.com/pteich/traefik/provider"
	"github.com/pteich/traefik/safe"
	"github.com/pteich/traefik/types"
)

// Provider holds common configurations of key-value providers.
type Provider struct {
	provider.BaseProvider `mapstructure:",squash" export:"true"`
	Endpoint              string           `description:"Comma separated server endpoints"`
	Prefix                string           `description:"Prefix used for KV store" export:"true"`
	TLS                   *types.ClientTLS `description:"Enable TLS support" export:"true"`
	Username              string           `description:"KV Username"`
	Password              string           `description:"KV Password"`
	storeType             string
	kvClient              store.Store
}

// CreateStore create the K/V store
func (p *Provider) CreateStore(ctx context.Context, storeConfig any) (store.Store, error) {
	return valkeyrie.NewStore(ctx,
		p.storeType,
		strings.Split(p.Endpoint, ","),
		storeConfig,
	)
}

// SetStoreType storeType setter
func (p *Provider) SetStoreType(storeType string) {
	p.storeType = storeType
}

// SetKVClient kvClient setter
func (p *Provider) SetKVClient(kvClient store.Store) {
	p.kvClient = kvClient
}

func (p *Provider) watchKv(ctx context.Context, configurationChan chan<- types.ConfigMessage, prefix string, stop chan bool) error {
	wtCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	operation := func() error {
		events, err := p.kvClient.WatchTree(wtCtx, p.Prefix, nil)
		if err != nil {
			return fmt.Errorf("failed to KV WatchTree: %v", err)
		}
		for {
			select {
			case <-stop:
				return nil
			case _, ok := <-events:
				if !ok {
					return errors.New("watchtree channel closed")
				}
				configuration, errC := p.buildConfiguration()
				if errC != nil {
					return errC
				}

				if configuration != nil {
					configurationChan <- types.ConfigMessage{
						ProviderName:  p.storeType,
						Configuration: configuration,
					}
				}
			}
		}
	}

	notify := func(err error, time time.Duration) {
		log.Errorf("KV connection error: %+v, retrying in %s", err, time)
	}
	err := backoff.RetryNotify(safe.OperationWithRecover(operation), job.NewBackOff(backoff.NewExponentialBackOff()), notify)
	if err != nil {
		return fmt.Errorf("cannot connect to KV server: %v", err)
	}
	return nil
}

// Provide provides the configuration to traefik via the configuration channel
func (p *Provider) Provide(ctx context.Context, configurationChan chan<- types.ConfigMessage, pool *safe.Pool) error {
	operation := func() error {
		if _, err := p.kvClient.Exists(ctx, p.Prefix+"/qmslkjdfmqlskdjfmqlksjazçueznbvbwzlkajzebvkwjdcqmlsfj", nil); err != nil {
			return fmt.Errorf("failed to test KV store connection: %v", err)
		}
		if p.Watch {
			pool.Go(func(stop chan bool) {
				err := p.watchKv(ctx, configurationChan, p.Prefix, stop)
				if err != nil {
					log.Errorf("Cannot watch KV store: %v", err)
				}
			})
		}
		configuration, err := p.buildConfiguration()
		if err != nil {
			return err
		}

		configurationChan <- types.ConfigMessage{
			ProviderName:  string(p.storeType),
			Configuration: configuration,
		}
		return nil
	}
	notify := func(err error, time time.Duration) {
		log.Errorf("KV connection error: %+v, retrying in %s", err, time)
	}
	err := backoff.RetryNotify(safe.OperationWithRecover(operation), job.NewBackOff(backoff.NewExponentialBackOff()), notify)
	if err != nil {
		return fmt.Errorf("cannot connect to KV server: %v", err)
	}
	return nil
}
