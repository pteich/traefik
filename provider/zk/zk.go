package zk

import (
	"context"
	"fmt"
	"time"

	"github.com/kvtools/valkeyrie/store"
	"github.com/kvtools/zookeeper"

	"github.com/pteich/traefik/provider"
	"github.com/pteich/traefik/provider/kv"
	"github.com/pteich/traefik/safe"
	"github.com/pteich/traefik/types"
)

var _ provider.Provider = (*Provider)(nil)

// Provider holds configurations of the provider.
type Provider struct {
	kv.Provider `mapstructure:",squash" export:"true"`
}

// Init the provider
func (p *Provider) Init(ctx context.Context, constraints types.Constraints) error {
	err := p.Provider.Init(constraints)
	if err != nil {
		return err
	}

	s, err := p.CreateStore(ctx)
	if err != nil {
		return fmt.Errorf("failed to Connect to KV store: %v", err)
	}

	p.SetKVClient(s)
	return nil
}

// Provide allows the zk provider to Provide configurations to traefik
// using the given configuration channel.
func (p *Provider) Provide(ctx context.Context, configurationChan chan<- types.ConfigMessage, pool *safe.Pool) error {
	return p.Provider.Provide(ctx, configurationChan, pool)
}

// CreateStore creates the KV store
func (p *Provider) CreateStore(ctx context.Context) (store.Store, error) {
	storeConfig := &zookeeper.Config{
		ConnectionTimeout: 30 * time.Second,
		Username:          p.Username,
		Password:          p.Password,
	}

	return p.Provider.CreateStore(ctx, storeConfig)
}
