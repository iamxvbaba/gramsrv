// Package dialogs is the dialogs application service: dialog list, unread counts, pins, drafts.
// The first phase implements PG-backed dialog summary queries for empty accounts; the real
// message loop is left to the second phase.
package dialogs
