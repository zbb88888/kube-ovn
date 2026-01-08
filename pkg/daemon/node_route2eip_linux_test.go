package daemon

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kubeovnv1 "github.com/kubeovn/kube-ovn/pkg/apis/kubeovn/v1"
)

func TestGenerateMacvlanName(t *testing.T) {
	tests := []struct {
		name     string
		cidr     string
		expected string
	}{
		{
			name:     "simple IPv4 CIDR",
			cidr:     "192.168.1.0/24",
			expected: "macvl19216810",
		},
		{
			name:     "shorter CIDR",
			cidr:     "10.0.0.0/8",
			expected: "macvl10000",
		},
		{
			name:     "longer CIDR",
			cidr:     "172.16.100.0/24",
			expected: "macvl172161000",
		},
		{
			name:     "CIDR with /32 mask",
			cidr:     "192.168.1.100/32",
			expected: "macvl1921681100",
		},
		{
			name:     "truncation for long network",
			cidr:     "255.255.255.255/32",
			expected: "macvl2552552552",
		},
		{
			name:     "IPv6 CIDR",
			cidr:     "2001:db8::/32",
			expected: "macvl2001db8",
		},
		{
			name:     "IPv6 truncation",
			cidr:     "2001:db8:abcd:1234::/64",
			expected: "macvl2001db8abc",
		},
		{
			name:     "dual-stack uses first CIDR",
			cidr:     "10.0.0.0/24,2001:db8::/64",
			expected: "macvl10000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := generateMacvlanName(tt.cidr)
			assert.LessOrEqual(t, len(result), 15, "interface name should not exceed 15 chars")
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestParseEIPDestination(t *testing.T) {
	tests := []struct {
		name        string
		eip         string
		wantMask    int
		wantErr     bool
		errContains string
	}{
		{
			name:     "valid IPv4",
			eip:      "192.168.1.100",
			wantMask: 32,
			wantErr:  false,
		},
		{
			name:     "valid IPv6",
			eip:      "2001:db8::1",
			wantMask: 128,
			wantErr:  false,
		},
		{
			name:        "invalid IP",
			eip:         "invalid",
			wantErr:     true,
			errContains: "invalid EIP address",
		},
		{
			name:        "empty string",
			eip:         "",
			wantErr:     true,
			errContains: "invalid EIP address",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dst, err := parseEIPDestination(tt.eip)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, dst)
			ones, _ := dst.Mask.Size()
			assert.Equal(t, tt.wantMask, ones)
		})
	}
}

func TestShouldEnqueueIptablesEip(t *testing.T) {
	tests := []struct {
		name           string
		externalSubnet string
		ready          bool
		want           bool
	}{
		{
			name:           "ready with ExternalSubnet",
			externalSubnet: "external-subnet",
			ready:          true,
			want:           true,
		},
		{
			name:           "ready without ExternalSubnet",
			externalSubnet: "",
			ready:          true,
			want:           false,
		},
		{
			name:           "not ready with ExternalSubnet",
			externalSubnet: "external-subnet",
			ready:          false,
			want:           false,
		},
		{
			name:           "not ready without ExternalSubnet",
			externalSubnet: "",
			ready:          false,
			want:           false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eip := &kubeovnv1.IptablesEIP{
				Spec: kubeovnv1.IptablesEIPSpec{
					ExternalSubnet: tt.externalSubnet,
				},
				Status: kubeovnv1.IptablesEIPStatus{
					Ready: tt.ready,
				},
			}
			assert.Equal(t, tt.want, shouldEnqueueIptablesEip(eip))
		})
	}
}
