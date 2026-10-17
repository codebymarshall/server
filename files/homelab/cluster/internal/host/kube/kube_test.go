package kube

import (
	"testing"

	"github.com/projectbluefin/server/files/homelab/cluster/internal/host/hosttest"
)

func TestInterlock(t *testing.T) {
	for _, tc := range []struct {
		states map[string]string
		want   int
		says   string
	}{
		{map[string]string{}, 0, ""},
		{map[string]string{"kubelet.service": "failed", "k0sworker.service": "inactive"}, 0, ""},
		{map[string]string{"kubelet.service": "active"}, 1, "kubelet.service is active: leaving the reboot to kured\n"},
		{map[string]string{"k0scontroller.service": "reloading"}, 1, "k0scontroller.service is reloading: leaving the reboot to kured\n"},
		{map[string]string{"k0sworker.service": "deactivating", "kubelet.service": "activating"}, 1, "kubelet.service is activating: leaving the reboot to kured\n"},
		{map[string]string{"k0sworker.service": "refreshing"}, 1, "k0sworker.service is refreshing: leaving the reboot to kured\n"},
	} {
		h := hosttest.New(nil, func(argv []string) (string, int) {
			if s, ok := tc.states[argv[2]]; ok {
				return s + "\n", 3
			}
			return "inactive\n", 3
		})
		if got := Interlock(h.Host, nil); got != tc.want || h.Out.String() != tc.says {
			t.Errorf("%v: exit %d, %q", tc.states, got, h.Out.String())
		}
	}
}
