package procobserver

import "testing"

func TestCensusV3CityContextIdentity(t *testing.T) {
	row := CensusIdentity{Classification: "nonmanaged", PID: 21, PPID: 2, PGID: 21, StartTicks: "210", Name: "fixture", City: "/city", UIDs: [4]uint32{1000, 1000, 1000, 1000}, NoGCEnvironment: true, EnvironmentRevalidated: true, StatRevalidated: true, PIDFDBound: true, UIDsRevalidated: true}
	for _, context := range []string{"/city", "city-alias", ""} {
		t.Run("context "+context, func(t *testing.T) {
			v := row
			v.City = context
			if !validCensusIdentity(v, 9) {
				t.Fatalf("validated context %q rejected", context)
			}
		})
	}
	for name, mutate := range map[string]func(*CensusIdentity){
		"ownership key present even if empty": func(v *CensusIdentity) { v.NoGCEnvironment = false },
		"partial SID":                         func(v *CensusIdentity) { v.SessionID = "sid" },
		"partial template":                    func(v *CensusIdentity) { v.Template = "worker" },
		"partial epoch":                       func(v *CensusIdentity) { v.Epoch = 1 },
		"partial token":                       func(v *CensusIdentity) { v.InstanceTokenSHA256 = "token" },
		"declared root":                       func(v *CensusIdentity) { v.DeclaredRoot = true },
		"missing UID read":                    func(v *CensusIdentity) { v.UIDsRevalidated = false },
		"missing pidfd":                       func(v *CensusIdentity) { v.PIDFDBound = false },
		"missing environment read":            func(v *CensusIdentity) { v.EnvironmentRevalidated = false },
		"malformed context":                   func(v *CensusIdentity) { v.City = "/city\nother" },
	} {
		t.Run(name, func(t *testing.T) {
			v := row
			mutate(&v)
			if validCensusIdentity(v, 9) {
				t.Fatal("invalid ownership/read/root accepted as context")
			}
		})
	}
	changed := row
	changed.City = "/other"
	if sameFreshClassification(row, changed) {
		t.Fatal("city context mutation erased from repeated identity equality")
	}
}

func TestCensusV3CityContextCannotReplaceContinuingRoot(t *testing.T) {
	for _, scan := range []int{1, 2, 3} {
		c, errors := v3Fixture()
		root := &c.Scans[scan].Verified[0]
		root.Classification = "nonmanaged"
		root.SessionID, root.Template, root.InstanceTokenSHA256 = "", "", ""
		root.Epoch = 0
		root.NoGCEnvironment = true
		root.DeclaredRoot = false
		if ValidateCensusV3(c, errors, len(errors), false, 3, 9) == nil {
			t.Fatal("full root lost ownership while keeping city but retirement certificate survived")
		}
	}
}
