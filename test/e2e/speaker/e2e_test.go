package speaker

import (
	"flag"
	"testing"

	"github.com/onsi/ginkgo/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/test/e2e"
	k8sframework "k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/e2e/framework/config"

	"github.com/kubeovn/kube-ovn/test/e2e/framework"
)

const (
	speakerNamespace = "kube-system"
	speakerDaemonSet = "kube-ovn-speaker"
)

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

var _ = framework.Describe("[group:speaker]", func() {
	f := framework.NewDefaultFramework("speaker")
	f.SkipNamespaceCreation = true

	ginkgo.Context("[Speaker DaemonSet]", func() {
		ginkgo.It("should have speaker pods running in host network mode", func() {
			ginkgo.By("Getting speaker DaemonSet")
			dsClient := f.DaemonSetClientNS(speakerNamespace)
			ds := dsClient.Get(speakerDaemonSet)

			ginkgo.By("Verifying speaker DaemonSet uses host network")
			framework.ExpectTrue(ds.Spec.Template.Spec.HostNetwork, "speaker DaemonSet should use host network")

			ginkgo.By("Getting speaker pods")
			pods, err := dsClient.GetPods(ds)
			framework.ExpectNoError(err, "failed to get speaker pods")
			framework.ExpectNotEmpty(pods.Items, "speaker DaemonSet should have at least one pod")

			ginkgo.By("Verifying all speaker pods are Running")
			for _, pod := range pods.Items {
				framework.ExpectEqual(pod.Status.Phase, corev1.PodRunning,
					"speaker pod %s should be Running, but got %s", pod.Name, pod.Status.Phase)

				// Verify pod is using host network
				framework.ExpectTrue(pod.Spec.HostNetwork, "speaker pod %s should use host network", pod.Name)

				// Verify all containers are ready and have never restarted
				for _, cs := range pod.Status.ContainerStatuses {
					framework.ExpectTrue(cs.Ready, "container %s in pod %s should be ready", cs.Name, pod.Name)
					framework.ExpectEqual(cs.RestartCount, int32(0),
						"container %s in pod %s should have zero restarts, but got %d restarts",
						cs.Name, pod.Name, cs.RestartCount)
				}
			}

			ginkgo.By("Verifying speaker DaemonSet is fully rolled out")
			framework.ExpectEqual(ds.Status.NumberReady, ds.Status.DesiredNumberScheduled,
				"speaker DaemonSet should have all pods ready: ready=%d, desired=%d",
				ds.Status.NumberReady, ds.Status.DesiredNumberScheduled)
		})
	})
})
