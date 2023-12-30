package eureka

import (
	"context"
	"io"
	"time"

	"github.com/ArthurHlt/go-eureka-client/eureka"
	"github.com/cenkalti/backoff/v4"
	"github.com/containous/flaeg"

	"github.com/pteich/traefik/job"
	"github.com/pteich/traefik/log"
	"github.com/pteich/traefik/provider"
	"github.com/pteich/traefik/safe"
	"github.com/pteich/traefik/types"
)

// Provider holds configuration of the Provider provider.
type Provider struct {
	provider.BaseProvider `mapstructure:",squash" export:"true"`
	Endpoint              string         `description:"Eureka server endpoint"`
	Delay                 flaeg.Duration `description:"Override default configuration time between refresh (Deprecated)" export:"true"` // Deprecated
	RefreshSeconds        flaeg.Duration `description:"Override default configuration time between refresh" export:"true"`
}

// Init the provider
func (p *Provider) Init(ctx context.Context, constraints types.Constraints) error {
	return p.BaseProvider.Init(constraints)
}

// Provide allows the eureka provider to provide configurations to traefik
// using the given configuration channel.
func (p *Provider) Provide(ctx context.Context, configurationChan chan<- types.ConfigMessage, pool *safe.Pool) error {
	eureka.GetLogger().SetOutput(io.Discard)

	operation := func() error {
		client := eureka.NewClient([]string{p.Endpoint})

		applications, err := client.GetApplications()
		if err != nil {
			log.Errorf("Failed to retrieve applications, error: %s", err)
			return err
		}

		configuration, err := p.buildConfiguration(applications)
		if err != nil {
			log.Errorf("Failed to build configuration for Provider, error: %s", err)
			return err
		}

		configurationChan <- types.ConfigMessage{
			ProviderName:  "eureka",
			Configuration: configuration,
		}

		ticker := time.NewTicker(time.Duration(p.RefreshSeconds))
		pool.Go(func(stop chan bool) {
			for {
				select {
				case t := <-ticker.C:
					log.Debugf("Refreshing Provider %s", t.String())
					applications, err := client.GetApplications()
					if err != nil {
						log.Errorf("Failed to retrieve applications, error: %s", err)
						continue
					}
					configuration, err := p.buildConfiguration(applications)
					if err != nil {
						log.Errorf("Failed to refresh Provider configuration, error: %s", err)
						continue
					}
					configurationChan <- types.ConfigMessage{
						ProviderName:  "eureka",
						Configuration: configuration,
					}
				case <-stop:
					return
				}
			}
		})
		return nil
	}

	err := backoff.RetryNotify(operation, job.NewBackOff(backoff.NewExponentialBackOff()), notify)
	if err != nil {
		log.Errorf("Cannot connect to Provider server %+v", err)
		return err
	}
	return nil
}

func notify(err error, time time.Duration) {
	log.Errorf("Provider connection error %+v, retrying in %s", err, time)
}
