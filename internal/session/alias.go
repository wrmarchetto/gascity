package session

import (
	"slices"
	"strings"
)

const aliasHistoryMetadataKey = "alias_history"

// AliasHistory returns previously assigned aliases preserved in session
// metadata. Empty values and duplicates are removed.
func AliasHistory(metadata map[string]string) []string {
	if len(metadata) == 0 {
		return nil
	}
	return normalizeAliasList(strings.Split(metadata[aliasHistoryMetadataKey], ","), "")
}

// UpdatedAliasMetadata returns the metadata mutations needed to set the current
// alias while preserving prior aliases for internal delivery continuity.
func UpdatedAliasMetadata(metadata map[string]string, nextAlias string) map[string]string {
	currentAlias := strings.TrimSpace(metadata["alias"])
	history := AliasHistory(metadata)
	if currentAlias != "" && currentAlias != nextAlias {
		history = append([]string{currentAlias}, history...)
	}
	history = normalizeAliasList(history, nextAlias)
	return map[string]string{
		"alias":                 strings.TrimSpace(nextAlias),
		aliasHistoryMetadataKey: strings.Join(history, ","),
	}
}

// UpdatedAliasMetadataFromInfo is the Info-fed sibling of UpdatedAliasMetadata:
// it computes the byte-identical alias/alias_history mutations from the projected
// Info.Alias and Info.AliasHistory. Those fields equal metadata["alias"] (verbatim)
// and AliasHistory(metadata) respectively, so a caller holding a projected Info in
// place of the raw metadata map produces the same result the raw form would.
func UpdatedAliasMetadataFromInfo(info Info, nextAlias string) map[string]string {
	currentAlias := strings.TrimSpace(info.Alias)
	history := info.AliasHistory
	if currentAlias != "" && currentAlias != nextAlias {
		history = append([]string{currentAlias}, history...)
	}
	history = normalizeAliasList(history, nextAlias)
	return map[string]string{
		"alias":                 strings.TrimSpace(nextAlias),
		aliasHistoryMetadataKey: strings.Join(history, ","),
	}
}

// PrunedAliasHistoryMetadata returns the alias_history mutation that drops name
// from the history recorded in metadata, and reports whether name was there to
// drop. Callers write the mutation only when it was, so a history that never
// held the name costs no store write per reconciler tick.
//
// It reads and writes alias_history and nothing else, which is what lets one
// helper serve both callers in the pool sync path: the raw session-bead
// metadata map on a tick that renames nothing, and the mutation map
// UpdatedAliasMetadata just produced on a tick that does. Composing over the
// mutation map is the load-bearing half -- the entry being pruned is usually
// the alias that rotation moved into history microseconds earlier, so a helper
// that only read stored metadata would miss the case this exists for.
//
// The reported bool is presence of name, NOT inequality between the joined
// result and the stored string, and the difference is deliberate. AliasHistory
// normalizes on every read, so a stored history that is merely denormalized
// ("nux, nux") already behaves identically to its normalized form; rewriting it
// would buy no observable change and would put an alias_history write on every
// pool session on the tick after any writer stored a loose value. Prune what
// the caller named and leave formatting alone.
//
// No caller prunes a session's CURRENT alias, and none should: this is the
// exception carved out for a name owned by a pool rather than by a session
// (see AssigneeIdentities), and a pool's slot holds its current alias under
// the city alias lock, which is the guarantee history has no equivalent of.
func PrunedAliasHistoryMetadata(metadata map[string]string, name string) (map[string]string, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, false
	}
	// AliasHistory returns trimmed, deduplicated, non-empty entries, so an
	// exact match against the trimmed name is the whole presence test.
	history := AliasHistory(metadata)
	if !slices.Contains(history, name) {
		return nil, false
	}
	return map[string]string{aliasHistoryMetadataKey: strings.Join(normalizeAliasList(history, name), ",")}, true
}

func normalizeAliasList(values []string, exclude string) []string {
	exclude = strings.TrimSpace(exclude)
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || value == exclude || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
