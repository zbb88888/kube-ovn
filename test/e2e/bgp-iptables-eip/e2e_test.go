package bgp_iptables_eip

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"strings"
	"testing"
	"time"

	nad "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/client/clientset/versioned"
	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/test/e2e"
	k8sframework "k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/e2e/framework/config"
	e2enode "k8s.io/kubernetes/test/e2e/framework/node"

	apiv1 "github.com/kubeovn/kube-ovn/pkg/apis/kubeovn/v1"
	"github.com/kubeovn/kube-ovn/pkg/util"
	"github.com/kubeovn/kube-ovn/test/e2e/framework"
	"github.com/kubeovn/kube-ovn/test/e2e/framework/docker"
	"github.com/kubeovn/kube-ovn/test/e2e/framework/kind"
)

const (
	dockerExtNet1Name      = "kube-ovn-ext-net1"
	vpcNatGWConfigMapName  = "ovn-vpc-nat-gw-config"
	networkAttachDefName   = "ovn-vpc-external-network"
	externalSubnetProvider = "ovn-vpc-external-network.kube-system"
	bgpRouterContainer     = "clab-bgp-router"
)

func setupNetworkAttachmentDefinition(
	f *framework.Framework,
	dockerExtNetNetwork *dockernetwork.Inspect,
	attachNetClient *framework.NetworkAttachmentDefinitionClient,
	subnetClient *framework.SubnetClient,
	externalNetworkName string,
	nicName string,
	provider string,
	dockerExtNetName string,
) {
	ginkgo.GinkgoHelper()

	ginkgo.By("Getting docker network " + dockerExtNetName)
	network, err := docker.NetworkInspect(dockerExtNetName)
	framework.ExpectNoError(err, "getting docker network "+dockerExtNetName)
	ginkgo.By("Getting or creating network attachment definition " + externalNetworkName)

	// Create network attachment configuration using structured data
	type ipamConfig struct {
		Type         string `json:"type"`
		ServerSocket string `json:"server_socket"`
		Provider     string `json:"provider"`
	}
	type nadConfig struct {
		CNIVersion string     `json:"cniVersion"`
		Type       string     `json:"type"`
		Master     string     `json:"master"`
		Mode       string     `json:"mode"`
		IPAM       ipamConfig `json:"ipam"`
	}

	config := nadConfig{
		CNIVersion: "0.3.0",
		Type:       "macvlan",
		Master:     nicName,
		Mode:       "bridge",
		IPAM: ipamConfig{
			Type:         "kube-ovn",
			ServerSocket: "/run/openvswitch/kube-ovn-daemon.sock",
			Provider:     provider,
		},
	}

	attachConfBytes, err := json.Marshal(config)
	framework.ExpectNoError(err, "marshaling network attachment configuration")
	attachConf := string(attachConfBytes)

	// Try to get existing NAD first
	nad, err := attachNetClient.NetworkAttachmentDefinitionInterface.Get(context.TODO(), externalNetworkName, metav1.GetOptions{})
	if err != nil && k8serrors.IsNotFound(err) {
		// NAD doesn't exist, create it
		attachNet := framework.MakeNetworkAttachmentDefinition(externalNetworkName, framework.KubeOvnNamespace, attachConf)
		nad = attachNetClient.Create(attachNet)
	} else {
		framework.ExpectNoError(err, "getting network attachment definition "+externalNetworkName)
	}

	ginkgo.By("Got network attachment definition " + nad.Name)

	ginkgo.By("Creating underlay macvlan subnet " + externalNetworkName)
	var cidrV4, cidrV6, gatewayV4, gatewayV6 string
	for _, config := range dockerExtNetNetwork.IPAM.Config {
		switch util.CheckProtocol(config.Subnet.Addr().String()) {
		case apiv1.ProtocolIPv4:
			if f.HasIPv4() {
				cidrV4 = config.Subnet.String()
				gatewayV4 = config.Gateway.String()
			}
		case apiv1.ProtocolIPv6:
			if f.HasIPv6() {
				cidrV6 = config.Subnet.String()
				gatewayV6 = config.Gateway.String()
			}
		}
	}
	cidr := make([]string, 0, 2)
	gateway := make([]string, 0, 2)
	if f.HasIPv4() {
		cidr = append(cidr, cidrV4)
		gateway = append(gateway, gatewayV4)
	}
	if f.HasIPv6() {
		cidr = append(cidr, cidrV6)
		gateway = append(gateway, gatewayV6)
	}
	excludeIPs := make([]string, 0, len(network.Containers)*2)
	for _, container := range network.Containers {
		if container.IPv4Address.IsValid() && f.HasIPv4() {
			excludeIPs = append(excludeIPs, container.IPv4Address.Addr().String())
		}
		if container.IPv6Address.IsValid() && f.HasIPv6() {
			excludeIPs = append(excludeIPs, container.IPv6Address.Addr().String())
		}
	}

	// Check if subnet already exists
	_, err = subnetClient.SubnetInterface.Get(context.TODO(), externalNetworkName, metav1.GetOptions{})
	if err != nil && k8serrors.IsNotFound(err) {
		// Subnet doesn't exist, create it
		macvlanSubnet := framework.MakeSubnet(externalNetworkName, "", strings.Join(cidr, ","), strings.Join(gateway, ","), "", provider, excludeIPs, nil, nil)
		_ = subnetClient.CreateSync(macvlanSubnet)
	} else {
		framework.ExpectNoError(err, "getting subnet "+externalNetworkName)
	}
}

func setupVpcNatGwTestEnvironment(
	f *framework.Framework,
	dockerExtNetNetwork *dockernetwork.Inspect,
	attachNetClient *framework.NetworkAttachmentDefinitionClient,
	subnetClient *framework.SubnetClient,
	vpcClient *framework.VpcClient,
	vpcNatGwClient *framework.VpcNatGatewayClient,
	vpcName string,
	overlaySubnetName string,
	vpcNatGwName string,
	natGwQosPolicy string,
	overlaySubnetV4Cidr string,
	overlaySubnetV4Gw string,
	lanIP string,
	dockerExtNetName string,
	externalNetworkName string,
	nicName string,
	provider string,
	skipNADSetup bool,
) {
	ginkgo.GinkgoHelper()

	if !skipNADSetup {
		setupNetworkAttachmentDefinition(
			f, dockerExtNetNetwork, attachNetClient,
			subnetClient, externalNetworkName, nicName, provider, dockerExtNetName)
	}

	ginkgo.By("Getting config map " + vpcNatGWConfigMapName)
	_, err := f.ClientSet.CoreV1().ConfigMaps(framework.KubeOvnNamespace).Get(context.Background(), vpcNatGWConfigMapName, metav1.GetOptions{})
	framework.ExpectNoError(err, "failed to get ConfigMap")

	ginkgo.By("Creating custom vpc " + vpcName)
	vpc := framework.MakeVpc(vpcName, lanIP, false, false, nil)
	_ = vpcClient.CreateSync(vpc)
	ginkgo.DeferCleanup(func() {
		ginkgo.By("Cleaning up custom vpc " + vpcName)
		vpcClient.DeleteSync(vpcName)
	})

	ginkgo.By("Creating custom overlay subnet " + overlaySubnetName)
	overlaySubnet := framework.MakeSubnet(overlaySubnetName, "", overlaySubnetV4Cidr, overlaySubnetV4Gw, vpcName, "", nil, nil, nil)
	_ = subnetClient.CreateSync(overlaySubnet)
	ginkgo.DeferCleanup(func() {
		ginkgo.By("Cleaning up custom overlay subnet " + overlaySubnetName)
		subnetClient.DeleteSync(overlaySubnetName)
	})

	ginkgo.By("Creating custom vpc nat gw " + vpcNatGwName)
	vpcNatGw := framework.MakeVpcNatGateway(vpcNatGwName, vpcName, overlaySubnetName, lanIP, externalNetworkName, natGwQosPolicy)
	_ = vpcNatGwClient.CreateSync(vpcNatGw, f.ClientSet)
	ginkgo.DeferCleanup(func() {
		ginkgo.By("Cleaning up custom vpc nat gw " + vpcNatGwName)
		vpcNatGwClient.DeleteSync(vpcNatGwName)
	})
}

// waitForIptablesEIPReady waits for an IptablesEIP to be ready
func waitForIptablesEIPReady(eipClient *framework.IptablesEIPClient, eipName string, timeout time.Duration) *apiv1.IptablesEIP {
	ginkgo.GinkgoHelper()
	var eip *apiv1.IptablesEIP
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		eip = eipClient.Get(eipName)
		if eip != nil && eip.Status.IP != "" && eip.Status.Ready {
			framework.Logf("IptablesEIP %s is ready with IP: %s", eipName, eip.Status.IP)
			return eip
		}
		time.Sleep(2 * time.Second)
	}
	framework.Failf("Timeout waiting for IptablesEIP %s to be ready", eipName)
	return nil
}

// waitForBGPRoutePresent waits for an IP to appear in BGP routes of the router container
func waitForBGPRoutePresent(ip string, timeout time.Duration) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func() error {
		stdout, stderr, err := docker.Exec(bgpRouterContainer, nil, "vtysh", "-c", "show ip route bgp")
		if err != nil {
			return fmt.Errorf("failed to query BGP routes: %w, stderr: %s", err, string(stderr))
		}
		output := string(stdout)
		framework.Logf("BGP routes in %s:\n%s", bgpRouterContainer, output)
		if !strings.Contains(output, ip) {
			return fmt.Errorf("IP %s not found in BGP routes", ip)
		}
		return nil
	}, timeout, 5*time.Second).Should(gomega.Succeed(), "IP %s should be announced via BGP", ip)
}

// waitForBGPRouteWithdrawn waits for an IP to be removed from BGP routes of the router container
func waitForBGPRouteWithdrawn(ip string, timeout time.Duration) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func() bool {
		stdout, _, err := docker.Exec(bgpRouterContainer, nil, "vtysh", "-c", "show ip route bgp")
		if err != nil {
			return false
		}
		return !strings.Contains(string(stdout), ip)
	}, timeout, 5*time.Second).Should(gomega.BeTrue(), "IP %s should be withdrawn from BGP", ip)
}

var _ = framework.OrderedDescribe("[group:bgp-iptables-eip]", func() {
	f := framework.NewDefaultFramework("bgp-iptables-eip")

	var attachNetClient *framework.NetworkAttachmentDefinitionClient
	var clusterName, vpcName, vpcNatGwName, overlaySubnetName string
	var vpcClient *framework.VpcClient
	var vpcNatGwClient *framework.VpcNatGatewayClient
	var subnetClient *framework.SubnetClient
	var iptablesEIPClient *framework.IptablesEIPClient

	var dockerExtNet1Network *dockernetwork.Inspect
	var net1NicName string

	ginkgo.BeforeAll(func() {
		// Check if BGP router container exists (created by make kind-init-bgp)
		ginkgo.By("Checking if BGP router container exists")
		containers, err := docker.ContainerList(map[string][]string{"name": {bgpRouterContainer}})
		framework.ExpectNoError(err, "listing containers")
		if len(containers) == 0 {
			ginkgo.Skip("BGP router container " + bgpRouterContainer + " not found - run 'make kind-init-bgp' first")
		}

		// Initialize clients manually for BeforeAll
		config, err := k8sframework.LoadConfig()
		framework.ExpectNoError(err, "loading kubeconfig")

		cs, err := clientset.NewForConfig(config)
		framework.ExpectNoError(err, "creating kubernetes clientset")

		// Initialize framework clients needed for BeforeAll
		if f.KubeOVNClientSet == nil {
			f.KubeOVNClientSet, err = framework.LoadKubeOVNClientSet()
			framework.ExpectNoError(err, "creating kube-ovn clientset")
		}
		if f.AttachNetClient == nil {
			nadClient, err := nad.NewForConfig(config)
			framework.ExpectNoError(err, "creating network attachment definition clientset")
			f.AttachNetClient = nadClient
		}

		attachNetClient = f.NetworkAttachmentDefinitionClientNS(framework.KubeOvnNamespace)
		subnetClient = f.SubnetClient()
		vpcClient = f.VpcClient()
		vpcNatGwClient = f.VpcNatGatewayClient()
		iptablesEIPClient = f.IptablesEIPClient()

		f.SkipVersionPriorTo(1, 15, "EIP BGP via node local route feature was introduced in v1.15")

		if clusterName == "" {
			ginkgo.By("Getting k8s nodes")
			k8sNodes, err := e2enode.GetReadySchedulableNodes(context.Background(), cs)
			framework.ExpectNoError(err)

			cluster, ok := kind.IsKindProvided(k8sNodes.Items[0].Spec.ProviderID)
			if !ok {
				ginkgo.Skip("underlay spec only runs on kind clusters")
			}
			clusterName = cluster
		}

		ginkgo.By("Ensuring docker network " + dockerExtNet1Name + " exists")
		network1, err := docker.NetworkCreate(dockerExtNet1Name, true, true)
		framework.ExpectNoError(err, "creating docker network "+dockerExtNet1Name)
		dockerExtNet1Network = network1

		ginkgo.By("Getting kind nodes")
		nodes, err := kind.ListNodes(clusterName, "")
		framework.ExpectNoError(err, "getting nodes in kind cluster")
		framework.ExpectNotEmpty(nodes)

		ginkgo.By("Connecting nodes to the docker network")
		err = kind.NetworkConnect(dockerExtNet1Network.ID, nodes)
		framework.ExpectNoError(err, "connecting nodes to network "+dockerExtNet1Name)

		ginkgo.By("Getting node links that belong to the docker network")
		nodes, err = kind.ListNodes(clusterName, "")
		framework.ExpectNoError(err, "getting nodes in kind cluster")

		ginkgo.By("Validating node links")
		gomega.Eventually(func() error {
			network1, err := docker.NetworkInspect(dockerExtNet1Name)
			if err != nil {
				return fmt.Errorf("failed to inspect docker network %s: %w", dockerExtNet1Name, err)
			}

			for _, node := range nodes {
				container, exists := network1.Containers[node.ID]
				if !exists || container.MacAddress.String() == "" {
					return fmt.Errorf("node %s not ready in network containers (exists=%v, MAC=%s)", node.ID, exists, container.MacAddress.String())
				}

				links, err := node.ListLinks()
				if err != nil {
					return fmt.Errorf("failed to list links on node %s: %w", node.Name(), err)
				}

				net1Mac := container.MacAddress
				var eth0Exist, net1Exist bool
				for _, link := range links {
					if link.IfName == "eth0" {
						eth0Exist = true
					}
					if link.Address == net1Mac.String() {
						net1NicName = link.IfName
						net1Exist = true
					}
				}

				if !eth0Exist {
					return fmt.Errorf("eth0 not found on node %s", node.Name())
				}
				if !net1Exist {
					return fmt.Errorf("net1 interface with MAC %s not found on node %s", net1Mac.String(), node.Name())
				}
				framework.Logf("Node %s has eth0 and net1 with MAC %s", node.Name(), net1Mac.String())
			}
			return nil
		}, 30*time.Second, 500*time.Millisecond).Should(gomega.Succeed(), "timed out waiting for all nodes to have their network interfaces ready")

		ginkgo.By("Creating shared NAD and subnet for all tests")
		setupNetworkAttachmentDefinition(
			f, dockerExtNet1Network, attachNetClient,
			subnetClient, networkAttachDefName, net1NicName,
			externalSubnetProvider, dockerExtNet1Name)

		ginkgo.DeferCleanup(func() {
			ginkgo.By("Waiting for all EIPs using subnet " + networkAttachDefName + " to be deleted")
			gomega.Eventually(func() int {
				eips, err := f.KubeOVNClientSet.KubeovnV1().IptablesEIPs().List(context.Background(), metav1.ListOptions{
					LabelSelector: fmt.Sprintf("%s=%s", util.SubnetNameLabel, networkAttachDefName),
				})
				if err != nil {
					framework.Logf("Failed to list EIPs: %v", err)
					return -1
				}
				if len(eips.Items) > 0 {
					framework.Logf("Still waiting for %d EIP(s) to be deleted", len(eips.Items))
				}
				return len(eips.Items)
			}, 2*time.Minute, 2*time.Second).Should(gomega.Equal(0), "All EIPs should be deleted before cleaning up subnet")

			ginkgo.By("Cleaning up shared macvlan underlay subnet " + networkAttachDefName)
			subnetClient.DeleteSync(networkAttachDefName)
			ginkgo.By("Cleaning up shared nad " + networkAttachDefName)
			attachNetClient.Delete(networkAttachDefName)

			// Clean up docker network infrastructure after all resources are deleted
			ginkgo.By("Getting nodes")
			nodes, err := kind.ListNodes(clusterName, "")
			framework.ExpectNoError(err, "getting nodes in cluster")

			if dockerExtNet1Network != nil {
				ginkgo.By("Disconnecting nodes from the docker network")
				err = kind.NetworkDisconnect(dockerExtNet1Network.ID, nodes)
				framework.ExpectNoError(err, "disconnecting nodes from network "+dockerExtNet1Name)
			}
		})
	})

	ginkgo.BeforeEach(func() {
		randomSuffix := framework.RandomSuffix()
		vpcName = "vpc-" + randomSuffix
		vpcNatGwName = "gw-" + randomSuffix
		overlaySubnetName = "overlay-subnet-" + randomSuffix
	})

	framework.ConformanceIt("BGP announcement via node local EIP route", func() {
		overlaySubnetV4Cidr := "10.0.6.0/24"
		overlaySubnetV4Gw := "10.0.6.1"
		lanIP := "10.0.6.254"
		natgwQoS := ""

		ginkgo.By("1. Creating VPC NAT Gateway environment")
		setupVpcNatGwTestEnvironment(
			f, dockerExtNet1Network, attachNetClient,
			subnetClient, vpcClient, vpcNatGwClient,
			vpcName, overlaySubnetName, vpcNatGwName, natgwQoS,
			overlaySubnetV4Cidr, overlaySubnetV4Gw, lanIP,
			dockerExtNet1Name, networkAttachDefName, net1NicName,
			externalSubnetProvider,
			true, // skipNADSetup: shared NAD created in BeforeAll
		)

		ginkgo.By("2. Creating IptablesEIP with BGP annotation")
		eipName := "bgp-eip-" + framework.RandomSuffix()
		eip := framework.MakeIptablesEIP(eipName, "", "", "", vpcNatGwName, "", "")
		eip.Annotations = map[string]string{util.BgpAnnotation: "true"}
		_ = iptablesEIPClient.CreateSync(eip)
		ginkgo.DeferCleanup(func() {
			ginkgo.By("Cleaning up EIP " + eipName)
			iptablesEIPClient.DeleteSync(eipName)
		})

		ginkgo.By("3. Waiting for EIP to be ready")
		readyEip := waitForIptablesEIPReady(iptablesEIPClient, eipName, 60*time.Second)
		framework.ExpectNotNil(readyEip, "EIP should be ready")
		eipV4 := readyEip.Status.IP
		framework.ExpectNotEmpty(eipV4, "EIP should have IP assigned")
		framework.Logf("EIP %s has IP: %s", eipName, eipV4)

		ginkgo.By("4. Verifying EIP is announced via BGP to clab-bgp-router")
		waitForBGPRoutePresent(eipV4, 90*time.Second)

		ginkgo.By("5. Creating a second EIP with BGP annotation")
		eip2Name := "bgp-eip2-" + framework.RandomSuffix()
		eip2 := framework.MakeIptablesEIP(eip2Name, "", "", "", vpcNatGwName, "", "")
		eip2.Annotations = map[string]string{util.BgpAnnotation: "true"}
		_ = iptablesEIPClient.CreateSync(eip2)
		ginkgo.DeferCleanup(func() {
			ginkgo.By("Cleaning up EIP " + eip2Name)
			iptablesEIPClient.DeleteSync(eip2Name)
		})

		readyEip2 := waitForIptablesEIPReady(iptablesEIPClient, eip2Name, 60*time.Second)
		framework.ExpectNotNil(readyEip2, "Second EIP should be ready")
		eip2V4 := readyEip2.Status.IP
		framework.Logf("Second EIP %s has IP: %s", eip2Name, eip2V4)

		// Verify second EIP is announced via BGP
		waitForBGPRoutePresent(eip2V4, 90*time.Second)

		ginkgo.By("6. Deleting first EIP and verifying BGP route is withdrawn")
		iptablesEIPClient.DeleteSync(eipName)

		// Verify first EIP is withdrawn from BGP
		waitForBGPRouteWithdrawn(eipV4, 90*time.Second)

		// Second EIP should still be in BGP routes
		waitForBGPRoutePresent(eip2V4, 10*time.Second)

		ginkgo.By("7. Test completed: BGP announcement via node local EIP route works correctly")
	})

	framework.ConformanceIt("EIP without BGP annotation should not be announced", func() {
		overlaySubnetV4Cidr := "10.0.7.0/24"
		overlaySubnetV4Gw := "10.0.7.1"
		lanIP := "10.0.7.254"
		natgwQoS := ""

		ginkgo.By("1. Creating VPC NAT Gateway environment")
		setupVpcNatGwTestEnvironment(
			f, dockerExtNet1Network, attachNetClient,
			subnetClient, vpcClient, vpcNatGwClient,
			vpcName, overlaySubnetName, vpcNatGwName, natgwQoS,
			overlaySubnetV4Cidr, overlaySubnetV4Gw, lanIP,
			dockerExtNet1Name, networkAttachDefName, net1NicName,
			externalSubnetProvider,
			true, // skipNADSetup: shared NAD created in BeforeAll
		)

		ginkgo.By("2. Creating IptablesEIP WITHOUT BGP annotation")
		eipName := "no-bgp-eip-" + framework.RandomSuffix()
		eip := framework.MakeIptablesEIP(eipName, "", "", "", vpcNatGwName, "", "")
		// No BGP annotation - this EIP should NOT be announced via BGP
		_ = iptablesEIPClient.CreateSync(eip)
		ginkgo.DeferCleanup(func() {
			ginkgo.By("Cleaning up EIP " + eipName)
			iptablesEIPClient.DeleteSync(eipName)
		})

		ginkgo.By("3. Waiting for EIP to be ready")
		readyEip := waitForIptablesEIPReady(iptablesEIPClient, eipName, 60*time.Second)
		framework.ExpectNotNil(readyEip, "EIP should be ready")
		eipV4 := readyEip.Status.IP
		framework.ExpectNotEmpty(eipV4, "EIP should have IP assigned")
		framework.Logf("EIP %s has IP: %s (without BGP annotation)", eipName, eipV4)

		ginkgo.By("4. Verifying EIP is NOT announced via BGP")
		// Use Consistently to verify the route never appears during the observation period
		gomega.Consistently(func() bool {
			stdout, _, err := docker.Exec(bgpRouterContainer, nil, "vtysh", "-c", "show ip route bgp")
			if err != nil {
				framework.Logf("Failed to query BGP routes: %v", err)
				return true // Treat errors as "not found" to continue observation
			}
			found := strings.Contains(string(stdout), eipV4)
			if found {
				framework.Logf("Unexpectedly found EIP %s in BGP routes:\n%s", eipV4, string(stdout))
			}
			return !found
		}, 30*time.Second, 5*time.Second).Should(gomega.BeTrue(),
			"EIP %s without BGP annotation should never appear in BGP routes", eipV4)

		ginkgo.By("5. Test completed: EIP without BGP annotation is correctly not announced")
	})
})

func init() {
	klog.SetOutput(ginkgo.GinkgoWriter)

	// Register flags.
	config.CopyFlags(config.Flags, flag.CommandLine)
	k8sframework.RegisterCommonFlags(flag.CommandLine)
	k8sframework.RegisterClusterFlags(flag.CommandLine)
}

func TestE2E(t *testing.T) {
	k8sframework.AfterReadingAllFlags(&k8sframework.TestContext)
	e2e.RunE2ETests(t)
}
