package speaker

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateMutuallyExclusiveModes(t *testing.T) {
	tests := []struct {
		name    string
		config  *Configuration
		wantErr bool
		errMsg  string
	}{
		{
			name: "both modes disabled",
			config: &Configuration{
				NatGwMode:        false,
				NodeRouteEIPMode: false,
			},
			wantErr: false,
		},
		{
			name: "only NatGwMode enabled",
			config: &Configuration{
				NatGwMode:        true,
				NodeRouteEIPMode: false,
			},
			wantErr: false,
		},
		{
			name: "only NodeRouteEIPMode enabled",
			config: &Configuration{
				NatGwMode:        false,
				NodeRouteEIPMode: true,
			},
			wantErr: false,
		},
		{
			name: "mutually exclusive modes",
			config: &Configuration{
				NatGwMode:        true,
				NodeRouteEIPMode: true,
			},
			wantErr: true,
			errMsg:  "--nat-gw-mode and --node-route-eip-mode are mutually exclusive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.validateMutuallyExclusiveModes()
			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateRequiredFlags(t *testing.T) {
	tests := []struct {
		name    string
		config  *Configuration
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid config",
			config: &Configuration{
				NeighborAddresses: []net.IP{net.ParseIP("10.0.0.1")},
				ClusterAs:         65001,
				NeighborAs:        65002,
				NodeName:          "node1",
				NodeRouteEIPMode:  true,
			},
			wantErr: false,
		},
		{
			name: "missing neighbor address",
			config: &Configuration{
				ClusterAs:  65001,
				NeighborAs: 65002,
			},
			wantErr: true,
			errMsg:  "at least one of --neighbor-address or --neighbor-ipv6-address must be specified",
		},
		{
			name: "missing cluster-as",
			config: &Configuration{
				NeighborAddresses: []net.IP{net.ParseIP("10.0.0.1")},
				NeighborAs:        65002,
			},
			wantErr: true,
			errMsg:  "--cluster-as must be specified",
		},
		{
			name: "missing neighbor-as",
			config: &Configuration{
				NeighborAddresses: []net.IP{net.ParseIP("10.0.0.1")},
				ClusterAs:         65001,
			},
			wantErr: true,
			errMsg:  "--neighbor-as must be specified",
		},
		{
			name: "node-route-eip-mode without node-name",
			config: &Configuration{
				NeighborAddresses: []net.IP{net.ParseIP("10.0.0.1")},
				ClusterAs:         65001,
				NeighborAs:        65002,
				NodeRouteEIPMode:  true,
				NodeName:          "",
			},
			wantErr: true,
			errMsg:  "--node-route-eip-mode requires --node-name to be specified",
		},
		{
			name: "valid config without NodeRouteEIPMode",
			config: &Configuration{
				NeighborAddresses: []net.IP{net.ParseIP("10.0.0.1")},
				ClusterAs:         65001,
				NeighborAs:        65002,
				NodeRouteEIPMode:  false,
				NodeName:          "", // NodeName not required when NodeRouteEIPMode is false
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.validateRequiredFlags()
			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
