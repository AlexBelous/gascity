//go:build darwin

package proctable

import "fmt"

func observeRoots() ([]ObservedRoot, error) {
	// The existing ps reader flattens argv and environment. kern.procargs2 can
	// silently omit environment for restricted processes as well (XNU
	// sysctl_procargsx). Neither proves absence of GC_SESSION_ID host-wide.
	// Refuse completeness rather than claim a free slot from that ambiguity.
	return []ObservedRoot{}, fmt.Errorf("complete process environment coverage unsupported on Darwin; ps argv/environment flattening and restricted-process environment omission cannot prove absence")
}
