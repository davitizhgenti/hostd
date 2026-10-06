// Package sdk defines the module contract shared by built-in modules and
// external modules speaking JSON-RPC over a unix socket.
//
// A module owns one or more namespaces (manifest field Owns), declares the
// actions it handles and the events it emits, and implements Module. The
// core gives it a Core to emit events, send actions to other modules, and
// subscribe to events. Every type here has a stable JSON form, because the
// same structures cross the HTTP API and the external-module socket.
package sdk
