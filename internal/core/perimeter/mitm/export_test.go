package mitm

// RefMatchesGlobForTest exposes the unexported refMatchesGlob for white-box
// unit tests in the mitm_test package (proxy_test.go).  Only compiled during
// `go test`.
func RefMatchesGlobForTest(pattern, ref string) bool {
	return refMatchesGlob(pattern, ref)
}
