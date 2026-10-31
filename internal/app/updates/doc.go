// Package updates is the update state machine and delivery: per-user pts/qts/seq/date,
// offline deltas (updates.getDifference), and online push. The first phase persists the
// initial empty state at auth_key scope; the message event queue comes in the second phase.
package updates
