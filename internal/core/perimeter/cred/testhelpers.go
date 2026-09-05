package cred

import "fmt"

// This file exposes registry mutators for tests in OTHER packages (notably
// internal/cli), which cannot reach agentRegistry or profiles directly and
// cannot use an export_test.go, since those are compiled only into this
// package's own test binary.
//
// Deliberately no "testing" import: this is a non-test file, so importing
// testing here would link the test framework — and its flag registration —
// into the shipped nexus3 binary. The helpers therefore return an unregister
// func rather than taking a *testing.T, and callers pair them with t.Cleanup.

// RegisterOAuthFormatForTest registers a synthetic CredentialFormat in
// agentRegistry with the supplied DefaultFromPathFn and ImportFromPathFn, and
// returns the func that removes it again.
//
// Callers must defer or t.Cleanup the returned func; leaving a registration
// behind changes dispatch for every later test in the process. Callers must
// also register a matching profile via [RegisterProfileForTest].
func RegisterOAuthFormatForTest(
	format CredentialFormat,
	defaultFromPath func(AgentProfile) string,
	importFromPath func(string) (*DedicatedCredStore, error),
) (unregister func()) {
	agentRegistry[format] = AgentRegistration{
		DefaultFromPathFn: defaultFromPath,
		ImportFromPathFn:  importFromPath,
	}
	return func() { delete(agentRegistry, format) }
}

// RegisterProfileForTest adds profile to the package-level profile registry and
// returns the func that removes it again. The profile's Name must not already
// be registered; a collision returns an error rather than silently replacing a
// real profile, which would make an unrelated test fail confusingly later.
//
// Callers must defer or t.Cleanup the returned func.
func RegisterProfileForTest(profile AgentProfile) (unregister func(), err error) {
	if _, exists := profiles[profile.Name]; exists {
		return nil, fmt.Errorf("cred: RegisterProfileForTest: profile %q already registered", profile.Name)
	}
	profiles[profile.Name] = profile
	return func() { delete(profiles, profile.Name) }, nil
}
