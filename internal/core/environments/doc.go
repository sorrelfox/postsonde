// Package environments manages a project's environments (ENV-01): named
// variable sets kept as environments/*.json in the project folder, which
// one of them is active, and the values of their secret variables.
//
// What goes where:
//
//	<project>/environments/<slug>.json            the environment (projectfile.Environment);
//	                                              secret variables have "value": ""
//	<local>/environments.json                     the active environment's ID
//	<local>/secrets/environments/<env key>.json   secret values by variable name (0600)
//	<local>/local-values/environments/<env key>.json
//	                                              local values by variable name (SEC-02,
//	                                              package localvalues): this computer's
//	                                              value of a variable, which replaces its
//	                                              shared value here
//
// <local> is the workspace's local side-file directory
// (workspace.Manager.LocalDir), never inside the project. The secrets file is
// a stopgap until secrets move to the OS keychain: it is plain JSON readable
// only by the user.
//
// A Store is bound to one workspace and is not safe for concurrent use;
// callers serialise access. Standard library only. See docs/environments.md.
package environments
