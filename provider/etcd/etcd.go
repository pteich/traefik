package etcd

import (
	"context"
	"fmt"
	"time"

	"github.com/kvtools/etcdv3"
	"github.com/kvtools/valkeyrie/store"

	"github.com/pteich/traefik/log"
	"github.com/pteich/traefik/provider"
	"github.com/pteich/traefik/provider/kv"
	"github.com/pteich/traefik/safe"
	"github.com/pteich/traefik/types"
)

var _ provider.Provider = (*Provider)(nil)

// Provider holds configurations of the provider.
type Provider struct {
	kv.Provider `mapstructure:",squash" export:"true"`
	UseAPIV3    bool `description:"Use ETCD API V3" export:"true"`
}

// Init the provider
func (p *Provider) Init(ctx context.Context, constraints types.Constraints) error {
	err := p.Provider.Init(constraints)
	if err != nil {
		return err
	}

	store, err := p.CreateStore(ctx)
	if err != nil {
		return fmt.Errorf("failed to Connect to KV store: %v", err)
	}

	p.SetKVClient(store)
	return nil
}

// Provide allows the etcd provider to Provide configurations to traefik
// using the given configuration channel.
func (p *Provider) Provide(ctx context.Context, configurationChan chan<- types.ConfigMessage, pool *safe.Pool) error {
	return p.Provider.Provide(ctx, configurationChan, pool)
}

// CreateStore creates the KV store
func (p *Provider) CreateStore(ctx context.Context) (store.Store, error) {
	if !p.UseAPIV3 {
		// TODO: Deprecated
		log.Warn("The ETCD API V2 is deprecated. Please use API V3 instead")
	}

	storeConfig := &etcdv3.Config{
		ConnectionTimeout: 30 * time.Second,
		Username:          p.Username,
		Password:          p.Password,
	}

	if p.TLS != nil {
		var err error
		storeConfig.TLS, err = p.TLS.CreateTLSConfig()
		if err != nil {
			return nil, err
		}
	}

	return p.Provider.CreateStore(ctx, storeConfig)
}
