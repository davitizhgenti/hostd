// Package core loads modules, routes each action through the pipeline
// (validate, authorize, prioritize, queue, execute, publish) to the module
// that owns it, and delivers events to listeners. It knows nothing about
// windows, sound or containers.
package core
