// Package app binds the core engine to the Wails desktop shell.
//
// It is one of only two places (with cmd/) allowed to import Wails. It
// exposes core functionality as bound services for the React frontend and
// translates between core types and the frontend's view models. It holds
// no protocol or storage logic of its own; that belongs in internal/core.
package app
