package marathon

import (
	"github.com/gambol99/go-marathon"
	"github.com/pteich/traefik/types"
)

func (p *Provider) buildConfiguration(applications *marathon.Applications) *types.Configuration {
	if p.TemplateVersion == 1 {
		return p.buildConfigurationV1(applications)
	}
	return p.buildConfigurationV2(applications)
}
