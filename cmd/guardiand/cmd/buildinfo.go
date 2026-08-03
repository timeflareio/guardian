package cmd

import (
	"runtime/debug"
)

// This repository stores no version number anywhere. The version of a build is
// whatever the git tag says it is, and Go reads that for us: since 1.18 the
// toolchain stamps the module version and VCS state into every binary at build
// time, with no linker flags and nothing to keep in step.
//
// That is deliberate rather than merely convenient. A version literal in source
// is a second source of truth that has to be bumped in lockstep with the tag,
// and the failure is silent when it is not — this daemon shipped a hardcoded
// "1.0.0" that no build could override, because the symbol was a const and the
// linker's -X flag only writes to variables. With nothing stored, nothing can
// drift: cutting the tag *is* the version bump.
//
// What a build reports, by how it was produced:
//
//	built from a tag        v0.0.2
//	built past a tag        v0.0.3-0.20260803181448-2a8837ee4f57
//	uncommitted changes     …as above, suffixed +dirty
//	no VCS data available   (devel)
//
// The middle form is a Go pseudo-version: sortable, and encoding the base tag,
// the commit time and the commit itself. It is more informative than a release
// number, and it never claims to be a release it is not.

// buildVersion returns the version of this binary as recorded at build time.
// The returned string always carries its own "v" prefix when it has one, so
// callers must not add another.
func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Version == "" {
		return "unknown"
	}
	return info.Main.Version
}

// buildRevision returns the short commit this binary was built from, and
// whether the working tree carried uncommitted changes at the time. Both are
// empty when the binary was built without VCS information — which happens when
// the source is built outside a git checkout, so an empty revision is itself
// worth reporting rather than hiding.
func buildRevision() (rev string, modified bool) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	return rev, modified
}
