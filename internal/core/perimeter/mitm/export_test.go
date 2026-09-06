package mitm

// RefMatchesGlobForTest exposes the unexported refMatchesGlob for white-box
// unit tests in the mitm_test package (proxy_test.go).  Only compiled during
// `go test`.
func RefMatchesGlobForTest(pattern, ref string) bool {
	return refMatchesGlob(pattern, ref)
}

// LookupPolicyForTest exposes the unexported lookupPolicy for white-box unit
// tests in the mitm_test package (proxy_test.go).  Only compiled during
// `go test`.
func LookupPolicyForTest(pp PathPolicies, placeholder, host string) (HostPolicy, bool) {
	return lookupPolicy(pp, placeholder, host)
}

// MatchesDotSuffixForTest exposes matchesDotSuffix for white-box unit tests.
func MatchesDotSuffixForTest(host string, suffixes []string) bool {
	return matchesDotSuffix(host, suffixes)
}
