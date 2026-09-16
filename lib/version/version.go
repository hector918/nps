package version

// VERSION identifies this build. The client reports it to the server on
// connect and the client list displays it, so a build stamp here is how an
// operator tells which nodes are still running an old binary. It is a var
// rather than a const so the build can stamp it:
//
//	-X ehang.io/nps/lib/version.VERSION=0.26.10+g1a2b3c4
//
// Only this string is free-form. GetVersion below is compared byte for byte
// by the server, so stamping that one instead would stop this client from
// connecting to every server not updated in lockstep with it.
var VERSION = "v0.27.3-hz1"

// Fork names the repository this binary was built from. Upstream has been
// unmaintained since 2021 and this tree carries fixes it never got, so every
// place a version is printed or reported says plainly which one it is.
const Fork = "hector918/nps"

// ForkMarker must appear in VERSION for every build from this fork, including
// the tag the release pipeline stamps in. The server uses it to tell whether a
// connected client understands the control messages this fork added -- an
// older client would misparse them -- so build.release.sh refuses to build a
// tag without it.
const ForkMarker = "-hz"

// Compulsory minimum version, Minimum downward compatibility to this version
//
// Do not touch this. The server compares it byte for byte against what a
// connecting client sends, so a fork marker here would lock this build out of
// every server not upgraded in the same breath. The fork identity belongs in
// VERSION and Fork above, which are only ever displayed.
func GetVersion() string {
	return "0.26.0"
}
