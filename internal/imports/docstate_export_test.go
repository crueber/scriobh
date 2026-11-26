package imports

// AttachTables and DerivedTables give the external tests the lists docstate.go keeps.
func AttachTables() []string {
	var out []string
	for _, a := range attachTables {
		out = append(out, a.table)
	}
	return out
}

func DerivedTables() map[string]string { return derivedTables }
