package configuration

import (
	"context"
	"encoding/json"

	"github.com/pteich/traefik/log"
	"github.com/pteich/traefik/provider"
	"github.com/pteich/traefik/safe"
	"github.com/pteich/traefik/types"
)

// ProviderAggregator aggregate providers
type ProviderAggregator struct {
	providers   []provider.Provider
	constraints types.Constraints
}

// NewProviderAggregator return an aggregate of all the providers configured in GlobalConfiguration
func NewProviderAggregator(ctx context.Context, gc *GlobalConfiguration) ProviderAggregator {
	p := ProviderAggregator{
		constraints: gc.Constraints,
	}
	if gc.Docker != nil {
		p.quietAddProvider(ctx, gc.Docker)
	}
	if gc.Marathon != nil {
		p.quietAddProvider(ctx, gc.Marathon)
	}
	if gc.File != nil {
		p.quietAddProvider(ctx, gc.File)
	}
	if gc.Rest != nil {
		p.quietAddProvider(ctx, gc.Rest)
	}
	if gc.Consul != nil {
		p.quietAddProvider(ctx, gc.Consul)
	}
	if gc.ConsulCatalog != nil {
		p.quietAddProvider(ctx, gc.ConsulCatalog)
	}
	if gc.Etcd != nil {
		p.quietAddProvider(ctx, gc.Etcd)
	}
	if gc.Zookeeper != nil {
		p.quietAddProvider(ctx, gc.Zookeeper)
	}
	if gc.Boltdb != nil {
		p.quietAddProvider(ctx, gc.Boltdb)
	}
	if gc.Kubernetes != nil {
		p.quietAddProvider(ctx, gc.Kubernetes)
	}
	if gc.Mesos != nil {
		p.quietAddProvider(ctx, gc.Mesos)
	}
	if gc.Eureka != nil {
		p.quietAddProvider(ctx, gc.Eureka)
	}
	if gc.ECS != nil {
		p.quietAddProvider(ctx, gc.ECS)
	}
	if gc.Rancher != nil {
		p.quietAddProvider(ctx, gc.Rancher)
	}
	if gc.DynamoDB != nil {
		p.quietAddProvider(ctx, gc.DynamoDB)
	}
	if gc.ServiceFabric != nil {
		//p.quietAddProvider(gc.ServiceFabric)
	}
	return p
}

func (p *ProviderAggregator) quietAddProvider(ctx context.Context, provider provider.Provider) {
	err := p.AddProvider(ctx, provider)
	if err != nil {
		log.Errorf("Error initializing provider %T: %v", provider, err)
	}
}

// AddProvider add a provider in the providers map
func (p *ProviderAggregator) AddProvider(ctx context.Context, provider provider.Provider) error {
	err := provider.Init(ctx, p.constraints)
	if err != nil {
		return err
	}
	p.providers = append(p.providers, provider)
	return nil
}

// Init the provider
func (p ProviderAggregator) Init(ctx context.Context, _ types.Constraints) error {
	return nil
}

// Provide call the provide method of every providers
func (p ProviderAggregator) Provide(ctx context.Context, configurationChan chan<- types.ConfigMessage, pool *safe.Pool) error {
	for _, p := range p.providers {
		jsonConf, err := json.Marshal(p)
		if err != nil {
			log.Debugf("Unable to marshal provider conf %T with error: %v", p, err)
		}
		log.Infof("Starting provider %T %s", p, jsonConf)
		currentProvider := p
		safe.Go(func() {
			err := currentProvider.Provide(ctx, configurationChan, pool)
			if err != nil {
				log.Errorf("Error starting provider %T: %v", p, err)
			}
		})
	}
	return nil
}
