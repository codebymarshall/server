// Package kube is the Kubernetes reboot interlock: kubelet (kubeadm sysext)
// or k0s starting, running or stopping means the node may carry workloads,
// so it reboots through kured (cordon, drain, one node at a time) instead.
package kube

import "github.com/projectbluefin/server/files/homelab/cluster/internal/host/sys"

// Units are the Kubernetes services the interlock looks at, in order.
var Units = []string{"kubelet.service", "k0scontroller.service", "k0sworker.service"}

// Busy returns the first Kubernetes unit that is active, activating,
// deactivating, reloading or refreshing, and its state. quiet adds
// 2>/dev/null to `systemctl is-active`.
func Busy(h *sys.Host, quiet bool) (unit, state string, busy bool) {
	for _, u := range Units {
		s, _ := h.Output(quiet, "systemctl", "is-active", u)
		switch s {
		case "active", "activating", "deactivating", "reloading", "refreshing":
			return u, s, true
		}
	}
	return "", "", false
}

// Interlock is systemd-sysupdate-reboot.service's ExecCondition=: exit 1
// (skip the nightly reboot) while a Kubernetes unit runs.
func Interlock(h *sys.Host, _ []string) int {
	if u, s, busy := Busy(h, false); busy {
		h.Printf("%s is %s: leaving the reboot to kured\n", u, s)
		return 1
	}
	return 0
}
