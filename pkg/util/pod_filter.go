package util

import v1 "k8s.io/api/core/v1"

// IgnoreCalicoPod returns true when the given annotations map contains the Calico pod IP key.
func IgnoreCalicoPod(pod *v1.Pod) bool {
	_, ok := pod.Annotations[CalicoPodIPAnnotation]
	return ok
}
