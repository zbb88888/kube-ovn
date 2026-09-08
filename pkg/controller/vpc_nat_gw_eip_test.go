package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubeovnv1 "github.com/kubeovn/kube-ovn/pkg/apis/kubeovn/v1"
	"github.com/kubeovn/kube-ovn/pkg/util"
)

// fakeGw returns a minimal VpcNatGateway CRD object for use in tests.
func fakeGw(name string) *kubeovnv1.VpcNatGateway {
	return &kubeovnv1.VpcNatGateway{
		Name: name,
		Spec: kubeovnv1.VpcNatGatewaySpec{},
	}
}

func TestSyncNatUIDLabels(t *testing.T) {
	t.Parallel()
	qos := &kubeovnv1.QoSPolicy{Name: "qos", UID: "qos-uid"}
	eip := &kubeovnv1.IptablesEIP{
		Name: "eip", UID: "eip-uid",
		Spec: kubeovnv1.IptablesEIPSpec{QoSPolicy: "qos"},
	}
	fip := &kubeovnv1.IptablesFIPRule{
		Name: "fip",
		Spec: kubeovnv1.IptablesFIPRuleSpec{EIP: "eip"},
	}
	fc, err := newFakeControllerWithOptions(t, &FakeControllerOptions{
		QoSPolicies:  []*kubeovnv1.QoSPolicy{qos},
		IptablesEips: []*kubeovnv1.IptablesEIP{eip},
	})
	require.NoError(t, err)
	_, err = fc.fakeController.config.KubeOvnClient.KubeovnV1().IptablesFIPRules().Create(context.Background(), fip, metav1.CreateOptions{})
	require.NoError(t, err)
	require.NoError(t, fc.fakeController.syncNatUIDLabels())

	gotEip, err := fc.fakeController.config.KubeOvnClient.KubeovnV1().IptablesEIPs().Get(context.Background(), "eip", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, "qos-uid", gotEip.Labels[util.QoSPolicyUIDLabel])
	gotFip, err := fc.fakeController.config.KubeOvnClient.KubeovnV1().IptablesFIPRules().Get(context.Background(), "fip", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, "eip-uid", gotFip.Labels[util.EipUIDLabel])
}

func TestFipRebindPreservesEipClaim(t *testing.T) {
	oldEnabled := vpcNatEnabled
	vpcNatEnabled = "true"
	t.Cleanup(func() { vpcNatEnabled = oldEnabled })

	newFip := func(statusGateway string) *kubeovnv1.IptablesFIPRule {
		return &kubeovnv1.IptablesFIPRule{
			Name: "fip", Labels: map[string]string{util.EipUIDLabel: "old-uid"},
			Spec:   kubeovnv1.IptablesFIPRuleSpec{EIP: "new-eip", InternalIP: "10.0.0.1"},
			Status: kubeovnv1.IptablesFIPRuleStatus{V4ip: "1.1.1.1", NatGwDp: statusGateway, InternalIP: "10.0.0.1", Ready: true},
		}
	}
	setup := func(t *testing.T, fip *kubeovnv1.IptablesFIPRule) *Controller {
		t.Helper()
		eip := &kubeovnv1.IptablesEIP{
			Name: "new-eip", UID: "new-uid",
			Spec:   kubeovnv1.IptablesEIPSpec{V4ip: "2.2.2.2", NatGwDp: "gw"},
			Status: kubeovnv1.IptablesEIPStatus{IP: "2.2.2.2"},
		}
		fc, err := newFakeControllerWithOptions(t, &FakeControllerOptions{
			VpcNatGateways: []*kubeovnv1.VpcNatGateway{fakeGw("gw")},
			IptablesEips:   []*kubeovnv1.IptablesEIP{eip},
			IptablesFips:   []*kubeovnv1.IptablesFIPRule{fip},
		})
		require.NoError(t, err)
		return fc.fakeController
	}
	getClaim := func(t *testing.T, c *Controller) string {
		t.Helper()
		fip, err := c.config.KubeOvnClient.KubeovnV1().IptablesFIPRules().Get(context.Background(), "fip", metav1.GetOptions{})
		require.NoError(t, err)
		return fip.Labels[util.EipUIDLabel]
	}

	t.Run("old claim remains when old rule cleanup fails", func(t *testing.T) {
		c := setup(t, newFip("gw"))
		require.Error(t, c.handleUpdateIptablesFip("fip"))
		require.Equal(t, "old-uid", getClaim(t, c))
	})
	t.Run("new claim is written before new rule creation", func(t *testing.T) {
		c := setup(t, newFip("retired-gw"))
		require.Error(t, c.handleUpdateIptablesFip("fip"))
		require.Equal(t, "new-uid", getClaim(t, c))
	})
}

func TestSyncNatUIDLabelsKeepsTerminatingBindings(t *testing.T) {
	now := metav1.Now()
	qos := &kubeovnv1.QoSPolicy{Name: "qos", UID: "qos-uid", DeletionTimestamp: &now}
	eip := &kubeovnv1.IptablesEIP{
		Name: "eip", UID: "eip-uid", DeletionTimestamp: &now,
		Labels: map[string]string{util.QoSLabel: "qos"},
		Spec:   kubeovnv1.IptablesEIPSpec{QoSPolicy: "qos"},
	}
	fip := &kubeovnv1.IptablesFIPRule{
		Name: "fip", Labels: map[string]string{util.EipV4IpLabel: "10.0.0.2"},
		Spec: kubeovnv1.IptablesFIPRuleSpec{EIP: "eip"},
	}
	fc, err := newFakeControllerWithOptions(t, &FakeControllerOptions{
		QoSPolicies:  []*kubeovnv1.QoSPolicy{qos},
		IptablesEips: []*kubeovnv1.IptablesEIP{eip},
	})
	require.NoError(t, err)
	_, err = fc.fakeController.config.KubeOvnClient.KubeovnV1().IptablesFIPRules().Create(context.Background(), fip, metav1.CreateOptions{})
	require.NoError(t, err)
	require.NoError(t, fc.fakeController.syncNatUIDLabels())

	gotEip, err := fc.fakeController.config.KubeOvnClient.KubeovnV1().IptablesEIPs().Get(context.Background(), "eip", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, "qos-uid", gotEip.Labels[util.QoSPolicyUIDLabel])
	gotFip, err := fc.fakeController.config.KubeOvnClient.KubeovnV1().IptablesFIPRules().Get(context.Background(), "fip", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, "eip-uid", gotFip.Labels[util.EipUIDLabel])
}

func TestQoSPolicyUID(t *testing.T) {
	t.Parallel()
	qos := &kubeovnv1.QoSPolicy{Name: "qos", UID: "qos-uid"}
	fc, err := newFakeControllerWithOptions(t, &FakeControllerOptions{QoSPolicies: []*kubeovnv1.QoSPolicy{qos}})
	require.NoError(t, err)

	uid, err := fc.fakeController.qosPolicyUID(qos.Name)
	require.NoError(t, err)
	require.Equal(t, string(qos.UID), uid)

	uid, err = fc.fakeController.qosPolicyUID("")
	require.NoError(t, err)
	require.Empty(t, uid)

	_, err = fc.fakeController.qosPolicyUID("missing")
	require.Error(t, err)
}

func TestIptablesEipDeleteHonorsUIDClaim(t *testing.T) {
	oldEnabled := vpcNatEnabled
	vpcNatEnabled = "false"
	t.Cleanup(func() { vpcNatEnabled = oldEnabled })
	newEip := func() *kubeovnv1.IptablesEIP {
		now := metav1.Now()
		return &kubeovnv1.IptablesEIP{
			Name: "eip-delete", UID: "eip-delete-uid", DeletionTimestamp: &now,
			Finalizers: []string{util.KubeOVNControllerFinalizer},
		}
	}

	t.Run("matching claim blocks finalizer removal", func(t *testing.T) {
		eip := newEip()
		fip := &kubeovnv1.IptablesFIPRule{
			Name: "fip", Labels: map[string]string{util.EipUIDLabel: string(eip.UID)},
		}
		fc, err := newFakeControllerWithOptions(t, &FakeControllerOptions{
			IptablesEips: []*kubeovnv1.IptablesEIP{eip}, IptablesFips: []*kubeovnv1.IptablesFIPRule{fip},
		})
		require.NoError(t, err)
		require.NoError(t, fc.fakeController.handleUpdateIptablesEip(eip.Name))
		got, err := fc.fakeController.config.KubeOvnClient.KubeovnV1().IptablesEIPs().Get(context.Background(), eip.Name, metav1.GetOptions{})
		require.NoError(t, err)
		require.Contains(t, got.Finalizers, util.KubeOVNControllerFinalizer)
	})

	t.Run("different UID does not block finalizer removal", func(t *testing.T) {
		eip := newEip()
		fip := &kubeovnv1.IptablesFIPRule{
			Name: "fip", Labels: map[string]string{util.EipUIDLabel: "other-uid", util.EipV4IpLabel: "same-address"},
		}
		fc, err := newFakeControllerWithOptions(t, &FakeControllerOptions{
			IptablesEips: []*kubeovnv1.IptablesEIP{eip}, IptablesFips: []*kubeovnv1.IptablesFIPRule{fip},
		})
		require.NoError(t, err)
		require.NoError(t, fc.fakeController.handleUpdateIptablesEip(eip.Name))
		got, err := fc.fakeController.config.KubeOvnClient.KubeovnV1().IptablesEIPs().Get(context.Background(), eip.Name, metav1.GetOptions{})
		if err == nil {
			require.NotContains(t, got.Finalizers, util.KubeOVNControllerFinalizer)
		} else {
			require.True(t, k8serrors.IsNotFound(err))
		}
	})
}

func TestGetIptablesEipNatUsesUID(t *testing.T) {
	eip := &kubeovnv1.IptablesEIP{Name: "eip", UID: "eip-uid"}
	makeLabels := func(uid string) map[string]string {
		return map[string]string{util.EipUIDLabel: uid}
	}
	fc, err := newFakeControllerWithOptions(t, &FakeControllerOptions{
		IptablesEips: []*kubeovnv1.IptablesEIP{eip},
		IptablesFips: []*kubeovnv1.IptablesFIPRule{
			{Name: "same", Labels: makeLabels("eip-uid")},
			{Name: "other", Labels: makeLabels("other-uid")},
		},
		IptablesDnatRules: []*kubeovnv1.IptablesDnatRule{
			{Name: "dnat", Labels: makeLabels("eip-uid")},
		},
	})
	require.NoError(t, err)
	got, err := fc.fakeController.getIptablesEipNat(eip)
	require.NoError(t, err)
	require.Equal(t, util.DnatUsingEip+","+util.FipUsingEip, got)
}

func TestNatGwDeleted(t *testing.T) {
	t.Parallel()

	t.Run("gateway CRD exists returns false", func(t *testing.T) {
		fc, err := newFakeControllerWithOptions(t, &FakeControllerOptions{
			VpcNatGateways: []*kubeovnv1.VpcNatGateway{fakeGw("test-gw")},
		})
		require.NoError(t, err)
		deleted, err := fc.fakeController.natGwDeleted("test-gw")
		require.NoError(t, err)
		require.False(t, deleted)
	})

	t.Run("gateway CRD missing returns true", func(t *testing.T) {
		fc, err := newFakeControllerWithOptions(t, nil)
		require.NoError(t, err)
		deleted, err := fc.fakeController.natGwDeleted("missing-gw")
		require.NoError(t, err)
		require.True(t, deleted)
	})
}

// TestDeleteEipInPod_NatGwGone verifies that deleteEipInPod returns nil (skips
// cleanup) when the VpcNatGateway CRD no longer exists, allowing the EIP to
// be finalized without an infinite retry loop.
func TestDeleteEipInPod_NatGwGone(t *testing.T) {
	t.Parallel()
	fc, err := newFakeControllerWithOptions(t, nil) // no VpcNatGateway
	require.NoError(t, err)
	err = fc.fakeController.deleteEipInPod("missing-gw", "10.0.0.1/24", "kube-system")
	require.NoError(t, err, "should skip cleanup when gateway CRD is gone")
}

// TestDeleteEipInPod_NatGwExistsPodMissing verifies that deleteEipInPod returns
// an error (triggering a reconcile retry) when the VpcNatGateway CRD exists but
// its pod is not yet available (e.g., being recreated).
func TestDeleteEipInPod_NatGwExistsPodMissing(t *testing.T) {
	t.Parallel()
	fc, err := newFakeControllerWithOptions(t, &FakeControllerOptions{
		VpcNatGateways: []*kubeovnv1.VpcNatGateway{fakeGw("test-gw")},
	})
	require.NoError(t, err)
	err = fc.fakeController.deleteEipInPod("test-gw", "10.0.0.1/24", "kube-system")
	require.Error(t, err, "should return error to retry when pod is temporarily absent")
}

// TestDelEipQoSInPod_NatGwGone verifies cleanup is skipped when gateway is gone.
func TestDelEipQoSInPod_NatGwGone(t *testing.T) {
	t.Parallel()
	fc, err := newFakeControllerWithOptions(t, nil)
	require.NoError(t, err)
	err = fc.fakeController.delEipQoSInPod("missing-gw", "10.0.0.1", "kube-system", kubeovnv1.QoSDirectionIngress)
	require.NoError(t, err, "should skip cleanup when gateway CRD is gone")
}

// TestDelEipQoSInPod_NatGwExistsPodMissing verifies that an error is returned
// when the gateway CRD exists but the pod is not ready.
func TestDelEipQoSInPod_NatGwExistsPodMissing(t *testing.T) {
	t.Parallel()
	fc, err := newFakeControllerWithOptions(t, &FakeControllerOptions{
		VpcNatGateways: []*kubeovnv1.VpcNatGateway{fakeGw("test-gw")},
	})
	require.NoError(t, err)
	err = fc.fakeController.delEipQoSInPod("test-gw", "10.0.0.1", "kube-system", kubeovnv1.QoSDirectionEgress)
	require.Error(t, err, "should return error to retry when pod is temporarily absent")
}

// TestEnqueueAddIptablesEip verifies that on the add path a terminating EIP is routed to the
// update queue (which runs deletion cleanup) instead of the add queue. This is what lets a
// stuck-terminating EIP be finalized after a controller restart, where the informer re-lists
// existing objects and fires only AddFunc.
func TestEnqueueAddIptablesEip(t *testing.T) {
	t.Parallel()
	c := &Controller{
		addIptablesEipQueue:    newTypedRateLimitingQueue[string]("AddIptablesEip", nil),
		updateIptablesEipQueue: newTypedRateLimitingQueue[string]("UpdateIptablesEip", nil),
	}
	t.Cleanup(c.addIptablesEipQueue.ShutDown)
	t.Cleanup(c.updateIptablesEipQueue.ShutDown)
	now := metav1.Now()
	assertEnqueueAddRouting(t, c.addIptablesEipQueue, c.updateIptablesEipQueue, c.enqueueAddIptablesEip,
		&kubeovnv1.IptablesEIP{Name: "live-eip"},
		&kubeovnv1.IptablesEIP{Name: "terminating-eip", DeletionTimestamp: &now},
	)
}
