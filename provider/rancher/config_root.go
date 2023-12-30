package rancher

import (
	"github.com/pteich/traefik/types"
)

func (p *Provider) buildConfiguration(containersInspected []rancherData) *types.Configuration {
	if p.TemplateVersion == 1 {
		return p.buildConfigurationV1(containersInspected)
	}
	return p.buildConfigurationV2(containersInspected)
}
